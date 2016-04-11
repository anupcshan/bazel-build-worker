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
)

func writeError(w http.ResponseWriter, statusCode int, err error) {
	w.WriteHeader(statusCode)
	fmt.Fprintf(w, "%v", err)
}

func ensureCached(cacheBaseUrl string, file *build_remote.FileEntry, workDir string) error {
	filePath := filepath.Join(workDir, file.Path)
	if _, err := os.Stat(filePath); err == nil || !os.IsNotExist(err) {
		return nil
	}

	dir := path.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fetchPath := fmt.Sprintf("%s/%s", cacheBaseUrl, file.Path)
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

func main() {
	port := flag.Int("port", 1234, "Port to listen on")
	cacheBaseUrl := flag.String("cache-base-url", "http://localhost:5701/hazelcast/rest/maps/hazelcast-build-cache", "Base of cache URL to connect to")
	tmpDirRoot := flag.String("tmp-dir-root", "/tmp/", "Root of temporary directory")

	flag.Parse()

	log.SetFlags(log.Lmicroseconds)

	listenAddr := fmt.Sprintf(":%d", *port)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		workReq := new(build_remote.RemoteWorkRequest)
		err := jsonpb.Unmarshal(r.Body, workReq)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		tmpDir, err := ioutil.TempDir(*tmpDirRoot, "workdir")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		log.Println(tmpDir)
		defer os.RemoveAll(tmpDir)

		for _, inputFile := range workReq.GetInputFiles() {
			if err := ensureCached(*cacheBaseUrl, inputFile, tmpDir); err != nil {
				log.Println(err)
			}
		}
	})

	err := http.ListenAndServe(listenAddr, nil)
	log.Fatal(err)
}
