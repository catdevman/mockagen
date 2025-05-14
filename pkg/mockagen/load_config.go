package mockagen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	yaml "gopkg.in/yaml.v3"
)

// LoadConfig -
func LoadConfig(p string) MockagenConfig {
	var allowedExt = []string{".yaml", ".json"}
	_, file := filepath.Split(p)
	if ext := filepath.Ext(file); !slices.Contains(allowedExt, ext) {
		fmt.Println("File type was:", ext)
		panic("Config files can only be yaml or json.")
	}
	f, err := os.Open(p)
	if err != nil {
		panic("Issue opening file.")
	}
	config := MockagenConfig{}
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
