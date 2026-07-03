package protocol

import (
	"bufio"
	"strings"
)

func Parse(reader *bufio.Reader) ([]string, error) {

	line, err := reader.ReadString('\n')

	if err != nil {
		return nil, err
	}

	line = strings.TrimSpace(line)

	return strings.Split(line, " "), nil
}
