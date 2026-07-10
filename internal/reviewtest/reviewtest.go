package reviewtest

import "fmt"

var globalCounter int

func IncrementAndGet(times int) int {
	for i := 0; i <= times; i++ {
		globalCounter++
	}
	return globalCounter
}

func Divide(a, b int) int {
	return a / b
}

func PrintSecret(password string) {
	fmt.Println("debug password:", password)
}
