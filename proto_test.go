package main

import (
	"bytes"
//	"fmt"
	"io"
//	"log" // Consider replacing log.Fatal with t.Fatal in tests
	"testing"

	"github.com/tidwall/resp"
	// You might need an assertion library or write your own helper for complex comparisons
	// For example: "github.com/stretchr/testify/assert"
	// "github.com/stretchr/testify/require"
)

func TestProtocol(t *testing.T) {
	raw := "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	rd := resp.NewReader(bytes.NewBufferString(raw))

	// Test functions should typically have assertions.
	// This loop will only run once for the given 'raw' data as it represents a single RESP value (an array).
	for {
		v, _, err := rd.ReadValue()
		if err != nil {
			if err == io.EOF {
				break // Expected end of input
			}
			// If it's an unexpected error, fail the test
			t.Fatalf("ReadValue() failed unexpectedly: %v", err)
		}

		t.Logf("Read RESP Value Type: %s", v.Type()) // Use t.Logf for verbose test output

		// Example Assertion: Check if the type is an Array
		if v.Type() != resp.Array {
			t.Errorf("Expected RESP type Array, got %s", v.Type())
		}

		// Example Assertion: Check array length
		arr := v.Array()
		expectedLen := 3
		if len(arr) != expectedLen {
			t.Errorf("Expected array length %d, got %d", expectedLen, len(arr))
		}

		// Example: Check specific elements if needed
		if len(arr) >= 1 && arr[0].String() != "SET" {
			t.Errorf("Expected first element to be 'SET', got '%s'", arr[0].String())
		}
		if len(arr) >= 2 && arr[1].String() != "foo" {
			t.Errorf("Expected second element to be 'foo', got '%s'", arr[1].String())
		}
		if len(arr) >= 3 && arr[2].String() != "bar" {
			t.Errorf("Expected third element to be 'bar', got '%s'", arr[2].String())
		}

		// If you want to print array contents (useful for debugging tests)
		if v.Type() == resp.Array {
			for i, val := range arr {
				t.Logf("#%d Type: %s, Value: %s", i, val.Type(), val.String())
			}
		}

		// DO NOT have a return statement like `return "foo", nil` here.
		// If you only expect one top-level value, the loop will naturally exit
		// on the next iteration when io.EOF is encountered.
	}

	// You could add further checks here if rd.ReadValue() was expected to be called multiple times
	// and you want to ensure it reached EOF correctly.
}


