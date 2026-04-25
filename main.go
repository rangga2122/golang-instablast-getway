package main

import (
	"embed"

	"github.com/azkazamdigital/wa-gateway/cmd"
)

//go:embed views/*
var embedViews embed.FS

func main() {
	cmd.Execute(embedViews)
}
