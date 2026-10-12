package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errJSON = errors.New("invalid JSON structure")

// JSON consumers must agree on one interpretation of signed claims and trusted
// request fields. Reject duplicate object keys, excess nesting and trailing data.
func uniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errJSON
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || seen[key] {
					return errJSON
				}
				seen[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errJSON
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errJSON
	}
	return nil
}

func requestFields(raw []byte) error {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return errJSON
	}
	for key := range root {
		if key != "customerId" && key != "createdBy" && key != "details" {
			return errJSON
		}
	}
	var lines []map[string]json.RawMessage
	if json.Unmarshal(root["details"], &lines) != nil {
		return errJSON
	}
	for _, line := range lines {
		for key := range line {
			if key != "productId" && key != "quantity" && key != "unitPriceCents" {
				return errJSON
			}
		}
	}
	return nil
}
