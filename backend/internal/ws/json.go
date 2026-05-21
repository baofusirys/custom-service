package ws

import "encoding/json"

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fromJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}
