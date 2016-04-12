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
	log.Println(err)
	w.WriteHeader(statusCode)
	workRes.Exception = err.Error()
	workRes.Success = false
	respond(w, workRes)
}

func ensureCached(cacheBaseURL string, file *remote.FileEntry, workDir string) error {
	filePath := filepath.Join(workDir, file.Path)
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

func writeCacheEntry(cacheBaseURL string, key string, data []byte) error {
	cacheEntry := new(remote.CacheEntry)
	cacheEntry.FileContent = data
	writePath := fmt.Sprintf("%s/%s", cacheBaseURL, key)
	log.Println(writePath)
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
	log.Println(writePath)
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

func HandleBuildRequest(w http.ResponseWriter, r *http.Request) {
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

	tmpDir, err := ioutil.TempDir(*tmpDirRoot, "workdir")
	if err != nil {
		writeError(w, http.StatusInternalServerError, workRes, err)
		return
	}

	log.Println(tmpDir)
	defer os.RemoveAll(tmpDir)

	for _, inputFile := range workReq.GetInputFiles() {
		if err := ensureCached(*cacheBaseURL, inputFile, tmpDir); err != nil {
			writeError(w, http.StatusInternalServerError, workRes, err)
		}
	}

	log.Println(workReq.Arguments)

	cmd := exec.Command(workReq.Arguments[0], workReq.Arguments[1:]...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = tmpDir

	env := []string{}
	for key, value := range workReq.GetEnvironment() {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	cmd.Env = env

	err = cmd.Run()
	if err != nil {
		workRes.Out = stdout.String()
		workRes.Err = stderr.String()
		writeError(w, http.StatusOK, workRes, err)
		return
	}

	workRes.Out = stdout.String()
	workRes.Err = stderr.String()

	outputActionCache := new(remote.CacheEntry)

	for _, outputFile := range workReq.GetOutputFiles() {
		filePath := filepath.Join(tmpDir, outputFile.Path)
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
			log.Println(outputFile)
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

	http.HandleFunc("/", HandleBuildRequest)

	err := http.ListenAndServe(listenAddr, nil)
	log.Fatal(err)
}

var (
	port         = flag.Int("port", 1234, "Port to listen on")
	cacheBaseURL = flag.String("cache-base-url", "http://localhost:5701/hazelcast/rest/maps/hazelcast-build-cache", "Base of cache URL to connect to")
	tmpDirRoot   = flag.String("tmp-dir-root", "/tmp/", "Root of temporary directory")
)
