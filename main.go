package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/anupcshan/bazel-build-worker/cache"
	"github.com/anupcshan/bazel-build-worker/remote"

	"github.com/golang/protobuf/proto"
)

func respond(w http.ResponseWriter, workRes *remote.RemoteWorkResponse) {
	b, err := proto.Marshal(workRes)
	if err != nil {
		log.Println(err)
	} else {
		w.Write(b)
	}
}

func writeError(w http.ResponseWriter, statusCode int, workRes *remote.RemoteWorkResponse, err error) {
	w.WriteHeader(statusCode)
	workRes.Exception = err.Error()
	workRes.Success = false
	respond(w, workRes)
}

func ensureCached(cacheBaseURL string, file *remote.FileEntry, cacheDir string) error {
	filePath := filepath.Join(cacheDir, file.ContentKey)
	if _, err := os.Stat(filePath); err == nil || !os.IsNotExist(err) {
		return nil
	}

	dir := path.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fetchPath := fmt.Sprintf("%s/%s", cacheBaseURL, file.ContentKey)
	if resp, err := http.Get(fetchPath); err != nil {
		return err
	} else {
		defer resp.Body.Close()
		perm := os.FileMode(0644)
		if file.Executable {
			perm = 0755
		}
		if f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, perm); err != nil {
			return err
		} else {
			defer f.Close()
			cacheEntry := new(remote.CacheEntry)

			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			err = proto.Unmarshal(b, cacheEntry)
			if err != nil {
				return err
			}
			_, err = f.Write(cacheEntry.FileContent)
			return err
		}
	}
	return nil
}

func linkCachedObject(relPath string, workDir string, cachePath string) error {
	filePath := filepath.Join(workDir, relPath)

	dir := path.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.Symlink(cachePath, filePath)
}

func writeCacheEntry(cacheBaseURL string, key string, data []byte) error {
	cacheEntry := new(remote.CacheEntry)
	cacheEntry.FileContent = data
	writePath := fmt.Sprintf("%s/%s", cacheBaseURL, key)
	b, err := proto.Marshal(cacheEntry)
	if err != nil {
		return err
	}
	body := bytes.NewBuffer(b)
	if resp, err := http.Post(writePath, "application/binary", body); err != nil {
		return err
	} else {
		resp.Body.Close()
	}

	return nil
}

func writeActionCacheEntry(cacheBaseURL string, key string, cacheEntry *remote.CacheEntry) error {
	writePath := fmt.Sprintf("%s/%s", cacheBaseURL, key)
	b, err := proto.Marshal(cacheEntry)
	if err != nil {
		return nil
	}
	if resp, err := http.Post(writePath, "application/binary", bytes.NewBuffer(b)); err != nil {
		return err
	} else {
		resp.Body.Close()
	}

	return nil
}

type BuildRequestHandler struct {
	hazelcastCache cache.Cache
	diskCache      *cache.DiskCache
}

func (bh *BuildRequestHandler) HandleBuildRequest(w http.ResponseWriter, r *http.Request) {
	workReq := new(remote.RemoteWorkRequest)
	workRes := new(remote.RemoteWorkResponse)

	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, workRes, err)
		return
	}

	err = proto.Unmarshal(b, workReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, workRes, err)
		return
	}

	workDir, err := ioutil.TempDir(*workdirRoot, "workdir")
	if err != nil {
		writeError(w, http.StatusInternalServerError, workRes, err)
		return
	}

	log.Println("Creating workdir:", workDir)
	defer os.RemoveAll(workDir)

	var wg sync.WaitGroup
	for _, inputFile := range workReq.GetInputFiles() {
		wg.Add(1)
		go func(key string, executable bool) {
			<-bh.diskCache.EnsureCached(key, executable, 10*time.Minute)
			wg.Done()
		}(inputFile.ContentKey, inputFile.Executable)
	}

	wg.Wait()

	for _, inputFile := range workReq.GetInputFiles() {
		if err := linkCachedObject(inputFile.Path, workDir, bh.diskCache.GetLink(inputFile.ContentKey)); err != nil {
			writeError(w, http.StatusInternalServerError, workRes, err)
			return
		}
	}

	if *logCommands {
		log.Println("Executing:", workReq.Arguments)
	}

	cmd := exec.Command(workReq.Arguments[0], workReq.Arguments[1:]...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = workDir

	env := []string{}
	for key, value := range workReq.GetEnvironment() {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	cmd.Env = env

	err = cmd.Run()
	if err != nil {
		if *logCommands {
			log.Println("===================")
			log.Println("Execution failed:")
			log.Println("STDOUT")
			log.Println(stdout.String())
			log.Println("STDERR")
			log.Println(stderr.String())
			log.Println("===================")
		}
		workRes.Out = stdout.String()
		workRes.Err = stderr.String()
		writeError(w, http.StatusOK, workRes, err)
		return
	}

	workRes.Out = stdout.String()
	workRes.Err = stderr.String()

	outputActionCache := new(remote.CacheEntry)

	for _, outputFile := range workReq.GetOutputFiles() {
		filePath := filepath.Join(workDir, outputFile.Path)
		if f, err := os.Open(filePath); err != nil {
			writeError(w, http.StatusOK, workRes, err)
			return
		} else if b, err := ioutil.ReadAll(f); err != nil {
			writeError(w, http.StatusOK, workRes, err)
			return
		} else {
			checksum := md5.Sum(b)
			writeCacheEntry(*cacheBaseURL, hex.EncodeToString(checksum[:md5.Size]), b)
			outputFile.ContentKey = hex.EncodeToString(checksum[:md5.Size])
			outputActionCache.Files = append(outputActionCache.Files, outputFile)
		}
	}

	writeActionCacheEntry(*cacheBaseURL, workReq.OutputKey, outputActionCache)

	workRes.Success = true
	w.WriteHeader(http.StatusOK)
	respond(w, workRes)
}

func main() {
	flag.Parse()

	log.SetFlags(log.Lmicroseconds | log.Lshortfile)

	listenAddr := fmt.Sprintf(":%d", *port)

	hc := cache.NewHazelcastCache(*cacheBaseURL)
	diskCache := cache.NewDiskCache(*cacheDir, hc)

	buildRequestHandler := &BuildRequestHandler{hazelcastCache: hc, diskCache: diskCache}

	http.HandleFunc("/", buildRequestHandler.HandleBuildRequest)

	err := http.ListenAndServe(listenAddr, nil)
	log.Fatal(err)
}

var (
	port         = flag.Int("port", 1234, "Port to listen on")
	cacheBaseURL = flag.String("cache-base-url", "http://localhost:5701/hazelcast/rest/maps/hazelcast-build-cache", "Base of cache URL to connect to")
	workdirRoot  = flag.String("workdir-root", "/tmp/", "Directory to create working subdirectories to execute actions in")
	cacheDir     = flag.String("cachedir", "/tmp/bazel-worker-cache", "Directory to store cached objects")
	logCommands  = flag.Bool("log-commands", true, "Log all command executions (include stdout/stderr in case of failures)")
)
