package deliver

import "os"

func readAll(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- test helper on a temp path
	if err != nil {
		return "", err
	}
	return string(b), nil
}
