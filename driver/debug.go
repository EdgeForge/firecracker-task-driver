package firevm

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"reflect"
	"runtime"
	"strconv"

	log "github.com/sirupsen/logrus"
)

const DefaultDebugPort = 9898

func GetFunctionName(i interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
}

func EnableDebugging() {
	port := DefaultDebugPort
	if pstr := os.Getenv("PORT"); pstr != "" {
		p, err := strconv.Atoi(pstr)
		if err != nil {
			log.Fatalln("Failed to parse port as int:", err)
		}
		port = p
	}

	go func() {
		listen := fmt.Sprintf("0.0.0.0:%d", port)
		log.Printf("debugger available at %s", listen)
		log.Println(http.ListenAndServe(listen, nil))
	}()
}
