package server

import (
	"io/ioutil"
	"os"
	"testing"
	"github.com/janelia-flyem/dvid/storage"
)

func loadConfigFile(t *testing.T, filename string) string {
	f, err := os.Open(filename)
	if err != nil {
		t.Fatalf("couldn't open TOML file %q: %v\n", filename, err)
	}
	data, err := ioutil.ReadAll(f)
	if err != nil {
		t.Fatalf("couldn't read TOML file %q: %v\n", filename, err)
	}
	return string(data)
}

func TestParseConfig(t *testing.T) {
	instanceCfg, logCfg, backendCfg, err := LoadConfig("../config-full.toml")
	if err != nil {
		t.Fatalf("bad TOML configuration: %v\n", err)
	}

	if instanceCfg.Gen != "sequential" || instanceCfg.Start != 100 {
		t.Errorf("Bad instance id retrieval of configuration: %v\n", instanceCfg)
	}

	if logCfg.Logfile != "/demo/logs/dvid.log" || logCfg.MaxSize != 500 || logCfg.MaxAge != 30 {
		t.Errorf("Bad logging configuration retrieval: %v\n", logCfg)
	}
	if backendCfg.DefaultKVDB != "raid6" || backendCfg.DefaultLog != "mutationlog" || backendCfg.KVStore["grayscale:99ef22cd85f143f58a623bd22aad0ef7"] != "kvautobus" {
		t.Errorf("Bad backend configuration retrieval: %v\n", backendCfg)
	}
}

func TestTOMLConfigAbsolutePath(t *testing.T) {
	// Initialize the filepath settings
	var c tomlConfig
	c.Server.WebClient = "dvid-distro/dvid-console"
	c.Logging.Logfile = "./foobar.log"

	c.Store = make(map[storage.Alias]storeConfig)

	c.Store["foo"] = make(storeConfig)
	c.Store["foo"]["engine"] = "basholeveldb"
	c.Store["foo"]["path"] = "foo-storage-db"

	c.Store["bar"] = make(storeConfig)
	c.Store["bar"]["engine"] = "basholeveldb"
	c.Store["bar"]["path"] = "/tmp/bar-storage-db" // Already absolute, should stay unchanged.

	// Convert relative paths to absolute
	c.ConvertPathsToAbsolute("/tmp/dvid-configs/myconfig.toml")

	// Checks
	if c.Server.WebClient != "/tmp/dvid-configs/dvid-distro/dvid-console" {
		t.Errorf("WebClient not correctly converted to absolute path: %s", c.Server.WebClient)
	}

	if c.Logging.Logfile != "/tmp/dvid-configs/foobar.log" {
		t.Errorf("Logfile not correctly converted to absolute path: %s", c.Logging.Logfile)
	}

	foo, _ := c.Store["foo"]
	path, _ := foo["path"]
	if path.(string) != "/tmp/dvid-configs/foo-storage-db" {
		t.Errorf("[store.foo].path not correctly converted to absolute path: %s", path)
	}

	engine, _ := foo["engine"]
	if engine.(string) != "basholeveldb" {
		t.Errorf("[store.foo].engine should not have been touched: %s", path)
	}

	bar, _ := c.Store["bar"]
	path, _ = bar["path"]
	if path.(string) != "/tmp/bar-storage-db" {
		t.Errorf("[store.bar].path was already absolute and should have been left unchanged: %s", path)
	}
}
