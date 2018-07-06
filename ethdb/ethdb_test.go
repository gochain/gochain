package ethdb_test

import (
	"io/ioutil"
)

func MustTempFile() string {
	f, err := ioutil.TempFile("", "gochain-")
	if err != nil {
		panic(err)
	}
	f.Close()
	return f.Name()
}

func MustTempDir() string {
	name, err := ioutil.TempDir("", "gochain-")
	if err != nil {
		panic(err)
	}
	return name
}
