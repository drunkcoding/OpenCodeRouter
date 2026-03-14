package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webAssets embed.FS

func getWebFS() http.FileSystem {
	fsys, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}
