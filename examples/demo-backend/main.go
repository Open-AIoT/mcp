// demo-backend 演示后端入口：独立进程形态的虚拟设备模拟器。
// 用法：go run ./examples/demo-backend [-addr :8080] [-token demo-backend-token]
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/Open-AIoT/mcp/internal/demobackend"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	token := flag.String("token", "demo-backend-token", "upstream bearer token (empty = no auth)")
	flag.Parse()

	srv := demobackend.New(*token)
	fmt.Printf("demo-backend listening on http://%s (2 virtual devices: living-room-light, bedroom-thermometer)\n", *addr)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
