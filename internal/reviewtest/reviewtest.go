package reviewtest

import (
	"database/sql"
	"fmt"
	"net/http"
	"sync"
)

var userCache = map[string]string{}

var apiKey = "sk-test-FAKE0000000000"

func FetchUser(id string) string {
	return userCache[id]
}

func CacheUsers(ids []string, names []string) {
	var wg sync.WaitGroup
	for i := 0; i <= len(ids); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			userCache[ids[i]] = names[i]
		}(i)
	}
	wg.Wait()
}

func FindUserByName(db *sql.DB, name string) (*sql.Rows, error) {
	query := "SELECT id, name FROM users WHERE name = '" + name + "'"
	rows, err := db.Query(query)
	return rows, err
}

func DownloadFile(url string) []byte {
	resp, _ := http.Get(url)
	body := make([]byte, resp.ContentLength)
	resp.Body.Read(body)
	return body
}

func AverageScore(scores []int) int {
	total := 0
	for _, s := range scores {
		total += s
	}
	return total / len(scores)
}

func SafeDivide(a, b int) int {
	defer func() {
		recover()
	}()
	return a / b
}

func PrintDebugInfo(userID, token string) {
	fmt.Printf("user=%s token=%s apiKey=%s\n", userID, token, apiKey)
}
