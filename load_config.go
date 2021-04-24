package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	yaml "gopkg.in/yaml.v3"
)

type strSlice []string

func (list strSlice) has(a string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

// LoadConfig -
func LoadConfig(p string) MockadooConfig {
	var allowedExt = strSlice{".yaml", ".json"}
	_, file := filepath.Split(p)
	if ext := filepath.Ext(file); !allowedExt.has(ext) {
		fmt.Println("File type was:", ext)
		panic("Config files can only be yaml or json.")
	}
	f, err := os.Open(p)
	if err != nil {
		panic("Issue opening file.")
	}
	config := MockadooConfig{}
	switch filepath.Ext(file) {
	case ".yaml":
		err = yaml.NewDecoder(bufio.NewReader(f)).Decode(&config)
	case ".json":
		err = json.NewDecoder(bufio.NewReader(f)).Decode(&config)
	}
	if err != nil {
		panic("Issue decoding config file")
	}

	return config
}
