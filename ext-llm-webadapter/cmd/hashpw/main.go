// hashpw is a tiny helper that prints a bcrypt hash for a password supplied on
// stdin or as $1. Use it to populate auth.users[].password_bcrypt in
// config.json without committing plaintext anywhere.
//
//	go run ./cmd/hashpw            # prompts on stdin
//	go run ./cmd/hashpw mypassword # one-shot
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"ext-llm-webadapter/internal/auth"
)

func main() {
	var pw string
	if len(os.Args) > 1 {
		pw = strings.Join(os.Args[1:], " ")
	} else {
		fmt.Fprint(os.Stderr, "password: ")
		s, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "read stdin:", err)
			os.Exit(1)
		}
		pw = strings.TrimRight(s, "\r\n")
	}
	if pw == "" {
		fmt.Fprintln(os.Stderr, "empty password")
		os.Exit(1)
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
