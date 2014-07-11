/*
	This file provides the highest-level view of the datastore via a Service.
*/

package datastore

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/janelia-flyem/dvid/dvid"
	"github.com/janelia-flyem/dvid/storage"
)

const (
	Version = "0.8"
)

var (
	// Map of mutexes at the granularity of repo node ID
	versionMutexes map[nodeID]*sync.Mutex
)

func init() {
	versionMutexes = make(map[nodeID]*sync.Mutex)
}

// The following identifiers are more compact than the global identifiers such as
// UUID or URLs, and therefore useful for compressing key sizes.

// InstanceID is a DVID server-specific identifier for data instances.  Each InstanceID
// is only used within one repo, so all key/values for a repo can be obtained by
// doing range queries on instances associated with a repo.
type InstanceID dvid.LocalID32

// Bytes returns a sequence of bytes encoding this InstanceID.
func (id InstanceID) Bytes() []byte {
	buf := make([]byte, LocalID32Size, LocalID32Size)
	binary.BigEndian.PutUint32(buf, uint32(id))
	return buf
}

// InstanceIDFromBytes returns a LocalID from the start of the slice and the number of bytes used.
// Note: No error checking is done to ensure byte slice has sufficient bytes for InstanceID.
func InstanceIDFromBytes(b []byte) (id InstanceID, length int) {
	return InstanceID(binary.BigEndian.Uint32(b)), LocalID32Size
}

// RepoID is a DVID server-specific identifier for a particular Repo.
type RepoID dvid.LocalID32

// Bytes returns a sequence of bytes encoding this RepoID.
func (id RepoID) Bytes() []byte {
	buf := make([]byte, LocalID32Size, LocalID32Size)
	binary.BigEndian.PutUint32(buf, uint32(id))
	return buf
}

// RepoIDFromBytes returns a RepoID from the start of the slice and the number of bytes used.
// Note: No error checking is done to ensure byte slice has sufficient bytes for RepoID.
func RepoIDFromBytes(b []byte) (id RepoID, length int) {
	return RepoID(binary.BigEndian.Uint32(b)), LocalID32Size
}

// VersionID is a DVID server-specific identifier for a particular version or
// node of a repo's DAG.
type VersionID dvid.LocalID32

// Bytes returns a sequence of bytes encoding this VersionID.
func (id VersionID) Bytes() []byte {
	buf := make([]byte, LocalID32Size, LocalID32Size)
	binary.BigEndian.PutUint32(buf, uint32(id))
	return buf
}

// VersionIDFromBytes returns a VersionID from the start of the slice and the number of bytes used.
// Note: No error checking is done to ensure byte slice has sufficient bytes for VersionID.
func VersionIDFromBytes(b []byte) (id VersionID, length int) {
	return VersionID(binary.BigEndian.Uint32(b)), LocalID32Size
}

const (
	MaxInstanceID = dvid.MaxLocalID32
	MaxRepoID     = dvid.MaxLocalID32
	MaxVersionID  = dvid.MaxLocalID32
)

// Versions returns a chart of version identifiers for data types and and DVID's datastore
// fixed at compile-time for this DVID executable
func Versions() string {
	var text string = "\nCompile-time version information for this DVID executable:\n\n"
	writeLine := func(name dvid.TypeString, version string) {
		text += fmt.Sprintf("%-15s   %s\n", name, version)
	}
	writeLine("Name", "Version")
	writeLine("DVID datastore", Version)
	writeLine("Storage driver", storage.Version)
	for _, datatype := range CompiledTypes {
		writeLine(datatype.DatatypeName(), datatype.DatatypeVersion())
	}
	return text
}

// Init creates a key-value datastore using default arguments.  Datastore
// configuration is stored as key/values in the datastore and also in a
// human-readable config file in the datastore directory.
func Init(directory string, create bool, config dvid.Config) error {
	fmt.Println("\nInitializing datastore at", directory)

	// Initialize the backend database
	engine, err := storage.NewKeyValueStore(directory, create, config)
	if err != nil {
		return fmt.Errorf("Error initializing datastore (%s): %s\n", directory, err.Error())
	}
	defer engine.Close()

	// Initial graph database from ordered key value
	kvDB, ok := engine.(storage.OrderedKeyValueDB)
	if !ok {
		return fmt.Errorf("Datastore at %s does not support key-value database ops.", directory)
	}
	// Initialize the graph backend database
	gengine, err := storage.NewGraphStore(directory, create, config, kvDB)
	if err != nil {
		return fmt.Errorf("Error initializing graph datastore (%s): %s\n", directory, err.Error())
	}
	defer gengine.Close()

	// Put empty Repos
	db, ok := engine.(storage.OrderedKeyValueSetter)
	if !ok {
		return fmt.Errorf("Datastore at %s does not support setting of key-value pairs!", directory)
	}
	repos := new(Repos)
	err = repos.Put(db)
	return err
}

// Service couples an open DVID storage engine and DVID repos.
type Service struct {
	*Repos

	// The backend storage which is private since we want to create an object
	// interface (e.g., cache object or UUID map) and hide DVID-specific keys.
	engine      storage.Engine
	kvDB        storage.OrderedKeyValueDB
	kvSetter    storage.OrderedKeyValueSetter
	kvGetter    storage.OrderedKeyValueGetter
	graphengine storage.Engine
	gDB         storage.GraphDB
	gSetter     storage.GraphSetter
	gGetter     storage.GraphGetter
}

type OpenErrorType int

const (
	ErrorOpening OpenErrorType = iota
	ErrorRepos
	ErrorDatatypeUnavailable
)

type OpenError struct {
	error
	ErrorType OpenErrorType
}

// Open opens a DVID datastore at the given path (directory, url, etc) and returns
// a Service that allows operations on that datastore.
func Open(path string) (s *Service, openErr *OpenError) {
	// Open the datastore
	create := false
	engine, err := storage.NewKeyValueStore(path, create, dvid.Config{})
	if err != nil {
		openErr = &OpenError{
			fmt.Errorf("Error opening datastore (%s): %s", path, err.Error()),
			ErrorOpening,
		}
		return
	}

	// Get interfaces this engine supports.
	kvGetter, ok := engine.(storage.OrderedKeyValueGetter)
	if !ok {
		openErr = &OpenError{
			fmt.Errorf("Opened datastore cannot get key-value pairs."),
			ErrorOpening,
		}
		return
	}
	kvSetter, ok := engine.(storage.OrderedKeyValueSetter)
	if !ok {
		openErr = &OpenError{
			fmt.Errorf("Opened datastore cannot set key-value pairs."),
			ErrorOpening,
		}
		return
	}
	kvDB, ok := engine.(storage.OrderedKeyValueDB)
	if !ok {
		openErr = &OpenError{
			fmt.Errorf("Opened datastore does not support key-value database ops."),
			ErrorOpening,
		}
		return
	}

	// Open the graph datastore (nothing happens if the graph key value store is used)
	gengine, err := storage.NewGraphStore(path, create, dvid.Config{}, kvDB)
	if err != nil {
		openErr = &OpenError{
			fmt.Errorf("Error opening graph datastore (%s): %s", path, err.Error()),
			ErrorOpening,
		}
		return
	}

	gSetter, ok := gengine.(storage.GraphSetter)
	if !ok {
		openErr = &OpenError{
			fmt.Errorf("Opened datastore cannot set graph objects."),
			ErrorOpening,
		}
		return
	}
	gGetter, ok := gengine.(storage.GraphGetter)
	if !ok {
		openErr = &OpenError{
			fmt.Errorf("Opened datastore cannot get graph objects."),
			ErrorOpening,
		}
		return
	}
	gDB, ok := gengine.(storage.GraphDB)
	if !ok {
		openErr = &OpenError{
			fmt.Errorf("Opened datastore does not support graph database ops."),
			ErrorOpening,
		}
		return
	}

	// Read this datastore's configuration
	repos := new(Repos)
	err = repos.Load(kvGetter)
	if err != nil {
		openErr = &OpenError{
			fmt.Errorf("Error reading repos: %s", err.Error()),
			ErrorRepos,
		}
		return
	}

	// Verify that the runtime configuration can be supported by this DVID's
	// compiled-in data types.
	dvid.Fmt(dvid.Debug, "Verifying datastore's supported types were compiled into DVID...\n")
	err = repos.VerifyCompiledTypes()
	if err != nil {
		openErr = &OpenError{
			fmt.Errorf("Data are not fully supported by this DVID server: %s", err.Error()),
			ErrorDatatypeUnavailable,
		}
		return
	}

	fmt.Printf("\nDatastoreService successfully opened: %s\n", path)
	s = &Service{repos, engine, kvDB, kvSetter, kvGetter, gengine, gDB, gSetter, gGetter}
	return
}

// StorageEngine returns a a key-value database interface.
func (s *Service) StorageEngine() storage.Engine {
	return s.engine
}

// KeyValueDB returns a key-value database interface.
func (s *Service) KeyValueDB() (storage.KeyValueDB, error) {
	return s.kvDB.(storage.KeyValueDB), nil
}

// KeyValueGetter returns a a key-value getter interface.
func (s *Service) KeyValueGetter() (storage.KeyValueGetter, error) {
	return s.kvGetter.(storage.KeyValueGetter), nil
}

// KeyValueSetter returns a a key-value setter interface.
func (s *Service) KeyValueSetter() (storage.KeyValueSetter, error) {
	return s.kvSetter.(storage.KeyValueSetter), nil
}

// OrderedKeyValueDB returns an ordered key-value database interface.
func (s *Service) OrderedKeyValueDB() (storage.OrderedKeyValueDB, error) {
	return s.kvDB, nil
}

// OrderedKeyValueGetter returns an ordered key-value getter interface.
func (s *Service) OrderedKeyValueGetter() (storage.OrderedKeyValueGetter, error) {
	return s.kvGetter, nil
}

// OrderedKeyValueSetter returns an ordered key-value setter interface.
func (s *Service) OrderedKeyValueSetter() (storage.OrderedKeyValueSetter, error) {
	return s.kvSetter, nil
}

// GraphStorageEngine returns a graph database interface.
func (s *Service) GraphStorageEngine() storage.Engine {
	return s.graphengine
}

// GraphDB returns a graph database interface.
func (s *Service) GraphDB() (storage.GraphDB, error) {
	return s.gDB, nil
}

// GraphGetter returns a GraphDB getter interface.
func (s *Service) GraphGetter() (storage.GraphGetter, error) {
	return s.gGetter, nil
}

// GraphSetter returns a GraphDB setter interface.
func (s *Service) GraphSetter() (storage.GraphSetter, error) {
	return s.gSetter, nil
}

// Batcher returns an interface that can create a new batch write.
func (s *Service) Batcher() (db storage.Batcher, err error) {
	var ok bool
	db, ok = s.kvSetter.(storage.Batcher)
	if !ok {
		err = fmt.Errorf("DVID key-value store does not support batch write")
	}
	return
}

// Shutdown closes a DVID datastore.
func (s *Service) Shutdown() {
	s.engine.Close()
}

// ReposListJSON returns JSON of a list of repos.
func (s *Service) ReposListJSON() (stringJSON string, err error) {
	if s.Repos == nil {
		stringJSON = "{}"
		return
	}
	var bytesJSON []byte
	bytesJSON, err = s.Repos.MarshalJSON()
	if err != nil {
		return
	}
	return string(bytesJSON), nil
}

// ReposAllJSON returns JSON of a list of repos.
func (s *Service) ReposAllJSON() (stringJSON string, err error) {
	if s.Repos == nil {
		stringJSON = "{}"
		return
	}
	var bytesJSON []byte
	bytesJSON, err = s.Repos.AllJSON()
	if err != nil {
		return
	}
	return string(bytesJSON), nil
}

// RepoJSON returns JSON for a particular repo referenced by a uuid.
func (s *Service) RepoJSON(root dvid.UUID) (stringJSON string, err error) {
	if s.Repos == nil {
		stringJSON = "{}"
		return
	}
	repo, err := s.Repos.RepoFromUUID(root)
	if err != nil {
		return "{}", err
	}
	stringJSON, err = repo.JSONString()
	return
}

// NOTE: Alterations of Repos should invoke persistence to the key-value database.
// All interaction with repos at the datastore.Service level should be using
// opaque UUID or the shortened repoID.

// NewRepo creates a new repo.
func (s *Service) NewRepo() (root dvid.UUID, repoID dvid.RepoLocalID, err error) {
	if s.Repos == nil {
		err = fmt.Errorf("Datastore service has no repos available")
		return
	}
	var repo *Repo
	repo, err = s.Repos.newRepo()
	if err != nil {
		return
	}
	err = s.Repos.Put(s.kvSetter) // Need to persist change to list of Repo
	if err != nil {
		return
	}
	err = repo.Put(s.kvSetter)
	root = repo.Root
	repoID = repo.RepoID
	return
}

// NewVersions creates a new version (child node) off of a LOCKED parent node.
// Will return an error if the parent node has not been locked.
func (s *Service) NewVersion(parent dvid.UUID) (u dvid.UUID, err error) {
	if s.Repos == nil {
		err = fmt.Errorf("Datastore service has no repos available")
		return
	}
	var repo *Repo
	repo, u, err = s.Repos.newChild(parent)
	if err != nil {
		return
	}
	err = repo.Put(s.kvSetter)
	return
}

// NewData adds data of given name and type to a repo specified by a UUID.
func (s *Service) NewData(u dvid.UUID, typename dvid.TypeString, dataname dvid.DataString, config dvid.Config) error {
	if s.Repos == nil {
		return fmt.Errorf("Datastore service has no repos available")
	}
	repo, err := s.Repos.RepoFromUUID(u)
	if err != nil {
		return err
	}
	err = repo.newData(dataname, typename, config)
	if err != nil {
		return err
	}
	return repo.Put(s.kvSetter)
}

// ModifyData modifies data of given name in repo specified by a UUID.
func (s *Service) ModifyData(u dvid.UUID, dataname dvid.DataString, config dvid.Config) error {
	if s.Repos == nil {
		return fmt.Errorf("Datastore service has no repos available")
	}
	repo, err := s.Repos.RepoFromUUID(u)
	if err != nil {
		return err
	}
	err = repo.modifyData(dataname, config)
	if err != nil {
		return err
	}
	return repo.Put(s.kvSetter)
}

// Locks the node with the given UUID.
func (s *Service) Lock(u dvid.UUID) error {
	if s.Repos == nil {
		return fmt.Errorf("Datastore service has no repos available")
	}
	repo, err := s.Repos.RepoFromUUID(u)
	if err != nil {
		return err
	}
	err = repo.Lock(u)
	if err != nil {
		return err
	}
	return repo.Put(s.kvSetter)
}

// SaveRepo forces this service to persist the repo with given UUID.
// It is useful when modifying repos internally.
func (s *Service) SaveRepo(u dvid.UUID) error {
	if s.Repos == nil {
		return fmt.Errorf("Datastore service has no repos available")
	}
	repo, err := s.Repos.RepoFromUUID(u)
	if err != nil {
		return err
	}
	return repo.Put(s.kvSetter)
}

// LocalIDFromUUID when supplied a UUID string, returns smaller sized local IDs that identify a
// repo and a version.
func (s *Service) LocalIDFromUUID(u dvid.UUID) (dID dvid.RepoLocalID, vID dvid.VersionLocalID, err error) {
	if s.Repos == nil {
		err = fmt.Errorf("Datastore service has no repos available")
		return
	}
	var repo *Repo
	repo, err = s.Repos.RepoFromUUID(u)
	if err != nil {
		return
	}
	dID = repo.RepoID
	var found bool
	vID, found = repo.VersionMap[u]
	if !found {
		err = fmt.Errorf("UUID (%s) not found in repo", u)
	}
	return
}

// NodeIDFromString when supplied a UUID string, returns the matched UUID as well as
// more compact local IDs that identify the repo and a version.  Partial matches
// are allowed, similar to RepoFromString.
func (s *Service) NodeIDFromString(str string) (u dvid.UUID, dID dvid.RepoLocalID,
	vID dvid.VersionLocalID, err error) {

	if s.Repos == nil {
		err = fmt.Errorf("Datastore service has no repos available")
		return
	}
	var repo *Repo
	repo, u, err = s.Repos.RepoFromString(str)
	if err != nil {
		return
	}
	dID = repo.RepoID
	vID = repo.VersionMap[u]
	return
}

// SupportedDataChart returns a chart (names/urls) of data referenced by this datastore
func (s *Service) SupportedDataChart() string {
	text := CompiledTypeChart()
	text += "Data currently referenced within this DVID datastore:\n\n"
	text += s.DataChart()
	return text
}

// About returns a chart of the code versions of compile-time DVID datastore
// and the runtime data types.
func (s *Service) About() string {
	var text string
	writeLine := func(name dvid.TypeString, version string) {
		text += fmt.Sprintf("%-15s   %s\n", name, version)
	}
	writeLine("Name", "Version")
	writeLine("DVID datastore", Version)
	writeLine("Storage backend", storage.Version)
	if s.Repos != nil {
		for _, dtype := range s.Repos.Datatypes() {
			writeLine(dtype.DatatypeName(), dtype.DatatypeVersion())
		}
	}
	return text
}

// TypesJSON returns the components and versions of DVID software available
// in this DVID server.
func (s *Service) TypesJSON() (jsonStr string, err error) {
	data := make(map[dvid.TypeString]string)
	for _, datatype := range CompiledTypes {
		data[datatype.DatatypeName()] = string(datatype.DatatypeUrl())
	}
	m, err := json.Marshal(data)
	if err != nil {
		return
	}
	jsonStr = string(m)
	return
}

// CurrentTypesJSON returns the components and versions of DVID software associated
// with the current repos in the service.
func (s *Service) CurrentTypesJSON() (jsonStr string, err error) {
	data := make(map[dvid.TypeString]string)
	if s.Repos != nil {
		for _, dtype := range s.Repos.Datatypes() {
			data[dtype.DatatypeName()] = dtype.DatatypeVersion()
		}
	}
	m, err := json.Marshal(data)
	if err != nil {
		return
	}
	jsonStr = string(m)
	return
}

// DataChart returns a text chart of data names and their types for this DVID server.
func (s *Service) DataChart() string {
	var text string
	if s.Repos == nil || len(s.Repos.list) == 0 {
		return "  No repos have been added to this datastore.\n"
	}
	writeLine := func(name dvid.DataString, version string, url UrlString) {
		text += fmt.Sprintf("%-15s  %-25s  %s\n", name, version, url)
	}
	for num, dset := range s.Repos.list {
		text += fmt.Sprintf("\nRepo %d (UUID = %s):\n\n", num+1, dset.Root)
		writeLine("Name", "Type Name", "Url")
		for name, data := range dset.DataMap {
			writeLine(name, string(data.DatatypeName())+" ("+data.DatatypeVersion()+")",
				data.DatatypeUrl())
		}
	}
	return text
}
