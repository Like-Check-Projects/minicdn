package main

import (
	"crypto/md5"
	"fmt"
)

func Md5str(v string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(v)))
}
