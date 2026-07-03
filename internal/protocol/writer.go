package protocol

import (
	"fmt"
	"net"
)

func Write(conn net.Conn, data any) {

	switch v := data.(type) {

	case string:
		fmt.Fprintf(conn, "+%s\r\n", v)

	case nil:
		fmt.Fprintf(conn, "$-1\r\n")

	default:
		fmt.Fprintf(conn, "+%v\r\n", v)
	}
}
