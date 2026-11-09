package main

import (
	"fmt"
	"time"
)

func main() {
	a := time.Date(2026, 11, 3, 14, 3, 4, 0, time.UTC)
	b := time.Date(2026, 11, 8, 14, 6, 0, 0, time.UTC)

	fmt.Println("a.Before(b) =", a.Before(b))

	f := time.Time.Before
	fmt.Printf("f 的类型    = %T\n", f)
	fmt.Println("f(a, b)     =", f(a, b))
}
