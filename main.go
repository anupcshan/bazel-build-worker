package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/anupcshan/bazel-build-worker/remote"

	"github.com/golang/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"
)

func writeError(w http.ResponseWriter, statusCode int, workRes *build_remote.RemoteWorkResponse, err error) {
	log.Println(err)
	w.WriteHeader(statusCode)
	workRes.Exception = err.Error()
	workRes.Success = false
	b, err := proto.Marshal(workRes)
	if err != nil {
		log.Println(err)
	} else {
		w.Write(b)
	}
}

func ensureCached(cacheBaseURL string, file *build_remote.FileEntry, workDir string) error {
	filePath := filepath.Join(workDir, file.Path)
	if _, err := os.Stat(filePath); err == nil || !os.IsNotExist(err) {
		return nil
	}

	dir := path.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fetchPath := fmt.Sprintf("%s/%s", cacheBaseURL, file.Path)
	if resp, err := http.Get(fetchPath); err != nil {
		return err
	} else {
		defer resp.Body.Close()
		if f, err := os.OpenFile(filePath, os.O_CREATE, 0666); err != nil {
			return err
		} else {
			defer f.Close()
			_, err := io.Copy(f, resp.Body)
			return err
		}
	}
	return nil
}

func HandleBuildRequest(w http.ResponseWriter, r *http.Request) {
	workReq := new(build_remote.RemoteWorkRequest)
	workRes := new(build_remote.RemoteWorkResponse)
	err := jsonpb.Unmarshal(r.Body, workReq)
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

	writeError(w, http.StatusOK, workRes, fmt.Errorf("Not built"))
}

func main() {
	flag.Parse()

	log.SetFlags(log.Lmicroseconds)

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
