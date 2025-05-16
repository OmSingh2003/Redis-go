package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/tidwall/resp"
)

func TestProtocol(t *testing.T) {
	raw := "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	rd := resp.NewReader(bytes.NewBufferString(raw))

	for {
		v, _, err := rd.ReadValue()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("ReadValue() failed unexpectedly: %v", err)
		}

		t.Logf("Read RESP Value Type: %s", v.Type()) 

		if v.Type() != resp.Array {
			t.Errorf("Expected RESP type Array, got %s", v.Type())
		}

		arr := v.Array()
		expectedLen := 3
		if len(arr) != expectedLen {
			t.Errorf("Expected array length %d, got %d", expectedLen, len(arr))
		}

		if len(arr) >= 1 && arr[0].String() != "SET" {
			t.Errorf("Expected first element to be 'SET', got '%s'", arr[0].String())
		}
		if len(arr) >= 2 && arr[1].String() != "foo" {
			t.Errorf("Expected second element to be 'foo', got '%s'", arr[1].String())
		}
		if len(arr) >= 3 && arr[2].String() != "bar" {
			t.Errorf("Expected third element to be 'bar', got '%s'", arr[2].String())
		}

		if v.Type() == resp.Array {
			for i, val := range arr {
				t.Logf("#%d Type: %s, Value: %s", i, val.Type(), val.String())
			}
		}

	}

}


