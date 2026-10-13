package main

import (
	"context"
	"fmt"
	"os"

	"git.subcult.tv/subculture-collective/project-template-go-worker-postgres-docker/internal/worker"
)

func main() {
	name := os.Getenv("WORKER_NAME")
	if name == "" {
		name = "worker"
	}
	fmt.Println(worker.RunOnce(context.Background(), name))
}
