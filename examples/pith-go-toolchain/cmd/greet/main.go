package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	name := "World"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	result := map[string]any{
		"greeting": fmt.Sprintf("Hello, %s!", name),
		"source":   "go-toolchain",
		"args":     os.Args[1:],
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
