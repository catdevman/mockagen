package mockagen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	yaml "gopkg.in/yaml.v3"
)

// LoadConfig -
func LoadConfig(p string) MockagenConfig {
	allowedExt := []string{".yaml", ".json", ".toml"}
	_, file := filepath.Split(p)
	if ext := filepath.Ext(file); !slices.Contains(allowedExt, ext) {
		fmt.Println("File type was:", ext)
		panic("Config files can only be yaml, json, or toml.")
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
	case ".toml":
		_, err = toml.NewDecoder(f).Decode(&config)
	}
	if err != nil {
		panic("Issue decoding config file")
	}

	return config
}
