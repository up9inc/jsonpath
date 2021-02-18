package jsonpath_test

import (
	"encoding/json"
	"fmt"

	"github.com/up9inc/jsonpath"
)

func ExampleGet() {
	raw := []byte(`{"hello":"world"}`)

	var data interface{}
	json.Unmarshal(raw, &data)

	out, err := jsonpath.Get(data, "$.hello")
	if err != nil {
		panic(err)
	}

	fmt.Print(out)
	// Output: world
}

func ExampleSet() {
	raw := []byte(`{"hello":"world"}`)

	var data interface{}
	json.Unmarshal(raw, &data)

	jsonpath.Set(data, "$.hello", "universe")

	out, err := jsonpath.Get(data, "$.hello")
	if err != nil {
		panic(err)
	}

	fmt.Print(out)
	// Output: universe
}

func ExamplePrepare() {
	raw := []byte(`{"hello":"world"}`)

	_, helloFilter, err := jsonpath.Prepare("$.hello")
	if err != nil {
		panic(err)
	}

	var data interface{}
	if err = json.Unmarshal(raw, &data); err != nil {
		panic(err)
	}

	out, err := helloFilter(data)
	if err != nil {
		panic(err)
	}

	fmt.Print(out)
	// Output: world
}
