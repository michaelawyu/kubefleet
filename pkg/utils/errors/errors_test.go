/*
Copyright 2026 The KubeFleet Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package errors

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// TestCommonUsePatterns demonstrates how the common error handling patterns are enabled by the errors package.
func TestCommonUsePatterns(t *testing.T) {
	var bytesBuf bytes.Buffer
	slogHandler := slog.NewTextHandler(&bytesBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := logr.FromSlogHandler(slogHandler)
	klog.SetLogger(logger)

	// Example 1: handling API server errors with rich context and structured logging.
	rawAPIServerErr := apierrors.NewAlreadyExists(schema.GroupResource{Group: "", Resource: "namespaces"}, "work")
	categorizedAPIServerErr := NewAPIServerError(rawAPIServerErr, "failed to create namespace", false, "k1", "v1")
	wrappedErr := Wraps(categorizedAPIServerErr, "additional high-level error description", "k2", "v2", "k3", "v3")
	klog.ErrorS(wrappedErr, "additional top/controller-level error description", Args(wrappedErr)...)
	// If there needs to be kv attributes at the top level as well:
	// klog.ErrorS(wrappedErr, "additional top/controller-level error description", append(Args(wrappedErr), "k4", "v4", "k5", "v5")...)
	klog.Flush()

	outputStr := readFromBuffer(t, &bytesBuf)

	// The full error output looks like the follows:
	// errors_test.go:53: time=2026-04-29T02:39:44.853+10:00 level=ERROR msg="additional top/controller-level error description" err="additional high-level error description: failed to create namespace: namespaces \"work\" already exists" errCategory=APIserver k1=v1 cached=false APIServerErrReason=AlreadyExists APIServerResHTTPCode=409 k2=v2 k3=v3
	wantSubStrings := []string{
		"msg=\"additional top/controller-level error description\"",
		"err=\"additional high-level error description: failed to create namespace: namespaces \\\"work\\\" already exists\"",
		"errCategory=APIserver",
		"k1=v1",
		"k2=v2",
		"k3=v3",
		"cached=false",
		"APIServerErrReason=AlreadyExists",
		"APIServerResHTTPCode=409",
	}
	for _, subStr := range wantSubStrings {
		if strings.Contains(outputStr, subStr) == false {
			t.Errorf("got %s\nwant to contain %s\n", outputStr, subStr)
		}
	}

	// Example 2: handling unexpected errors with rich context and structured logging.
	rawUtilityCallErr := fmt.Errorf("cannot calculate resource hash")
	categorizedUnexpectedErr := NewUnexpectedError(rawUtilityCallErr, "additional low-level error description", "k1", "v1")
	wrappedUnexpectedErr := Wraps(categorizedUnexpectedErr, "additional high-level error description", "k2", "v2")
	klog.ErrorS(wrappedUnexpectedErr, "additional top/controller-level error description", Args(wrappedUnexpectedErr)...)
	klog.Flush()

	outputStr = readFromBuffer(t, &bytesBuf)

	// The full error output looks like the follows:
	// errors_test.go:86: time=2026-04-29T02:49:04.560+10:00 level=ERROR msg="additional top/controller-level error description" err="additional high-level error description: additional low-level error description: cannot calculate resource hash" errCategory=unexpected k1=v1 callers="[{Function:github.com/kubefleet-dev/kubefleet/pkg/utils/errors.TestCommonUsePatterns File:SomeFilePath Line:74} {Function:testing.tRunner File:SomeFilePath Line:1934} {Function:runtime.goexit File:SomeFilePath Line:1268}]" k2=v2
	wantSubStrings = []string{
		"msg=\"additional top/controller-level error description\"",
		"err=\"additional high-level error description: additional low-level error description: cannot calculate resource hash\"",
		"errCategory=unexpected",
		"k1=v1",
		"callers=",
		"Function:github.com/kubefleet-dev/kubefleet/pkg/utils/errors.TestCommonUsePatterns",
		"k2=v2",
	}
	for _, subStr := range wantSubStrings {
		if strings.Contains(outputStr, subStr) == false {
			t.Errorf("got %s\nwant to contain %s\n", outputStr, subStr)
		}
	}

	// Example 3: handling uncategorized errors with rich context and structured logging.
	rawErr := fmt.Errorf("something went wrong")
	uncategorizedErr := Wraps(rawErr, "additional low-level error description", "k1", "v1")
	wrappedUncategorizedErr := Wraps(uncategorizedErr, "additional high-level error description", "k2", "v2")
	klog.ErrorS(wrappedUncategorizedErr, "additional top/controller-level error description", Args(wrappedUncategorizedErr)...)
	klog.Flush()

	outputStr = readFromBuffer(t, &bytesBuf)

	// The full error output looks like the follows:
	// errors_test.go:119: time=2026-04-29T02:59:04.560+10:00 level=ERROR msg="additional top/controller-level error description" err="additional high-level error description: additional low-level error description: something went wrong" errCategory=uncategorized k1=v1 k2=v2
	wantSubStrings = []string{
		"msg=\"additional top/controller-level error description\"",
		"err=\"additional high-level error description: additional low-level error description: something went wrong\"",
		"errCategory=uncategorized",
		"k1=v1",
		"k2=v2",
	}
	for _, subStr := range wantSubStrings {
		if strings.Contains(outputStr, subStr) == false {
			t.Errorf("got %s\nwant to contain %s\n", outputStr, subStr)
		}
	}
}

func TestErrorStringer(t *testing.T) {
	wrappedErr := fmt.Errorf("something went wrong")

	testCases := []struct {
		name    string
		err     *Error
		wantMsg string
	}{
		{
			name:    "no wrapped error, no description",
			err:     &Error{},
			wantMsg: "an unknown error occurred",
		},
		{
			name:    "no wrapped error, with description",
			err:     &Error{desc: "operation failed"},
			wantMsg: "operation failed",
		},
		{
			name:    "with wrapped error, no description",
			err:     &Error{wrapped: wrappedErr},
			wantMsg: "something went wrong",
		},
		{
			name:    "with wrapped error, with description",
			err:     &Error{wrapped: wrappedErr, desc: "operation failed"},
			wantMsg: "operation failed: something went wrong",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if got != tc.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

func TestWraps(t *testing.T) {
	plainErr := fmt.Errorf("plain error")
	categorizedChildErr := &Error{
		category: ErrCategoryUser,
		desc:     "child desc",
		attrs:    []interface{}{"ck1", "cv1"},
	}

	testCases := []struct {
		name    string
		err     error
		desc    string
		kvs     []interface{}
		wantErr *Error
	}{
		{
			name: "wrapping a plain error, no kvs",
			err:  plainErr,
			desc: "high level desc",
			wantErr: &Error{
				category: ErrCategoryUncategorized,
				wrapped:  plainErr,
				desc:     "high level desc",
			},
		},
		{
			name: "wrapping a plain error, with kvs",
			err:  plainErr,
			desc: "high level desc",
			kvs:  []interface{}{"k1", "v1"},
			wantErr: &Error{
				category: ErrCategoryUncategorized,
				wrapped:  plainErr,
				desc:     "high level desc",
				attrs:    []interface{}{"k1", "v1"},
			},
		},
		{
			name: "wrapping an *Error, no extra kvs",
			err:  categorizedChildErr,
			desc: "high level desc",
			wantErr: &Error{
				category: ErrCategoryUser,
				wrapped:  categorizedChildErr,
				desc:     "high level desc",
				attrs:    []interface{}{"ck1", "cv1"},
			},
		},
		{
			name: "wrapping an *Error, with extra kvs",
			err:  categorizedChildErr,
			desc: "high level desc",
			kvs:  []interface{}{"k2", "v2"},
			wantErr: &Error{
				category: ErrCategoryUser,
				wrapped:  categorizedChildErr,
				desc:     "high level desc",
				attrs:    []interface{}{"ck1", "cv1", "k2", "v2"},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := Wraps(tc.err, tc.desc, tc.kvs...)
			if diff := cmp.Diff(gotErr, tc.wantErr,
				cmp.AllowUnexported(Error{}),
				// Compare the wrapped error field by pointer equality so that cmp does not
				// need to recurse into unexported fields of standard-library error types.
				cmp.FilterPath(func(p cmp.Path) bool {
					return p.Last().String() == ".wrapped"
				}, cmp.Comparer(func(x, y error) bool {
					return x == y
				})),
			); diff != "" {
				t.Errorf("Wraps() mismatch (-got, +want):\n%s", diff)
			}
		})
	}
}

func TestArgs(t *testing.T) {
	testCases := []struct {
		name    string
		err     error
		kvs     []interface{}
		wantKVs []interface{}
	}{
		{
			name:    "plain error (not an *Error)",
			err:     fmt.Errorf("plain error"),
			wantKVs: nil,
		},
		{
			name:    "*Error with no category set, no attrs, no extra kvs",
			err:     &Error{},
			wantKVs: []interface{}{"errCategory", ErrCategoryUncategorized},
		},
		{
			name:    "*Error with explicit category, no attrs, no extra kvs",
			err:     &Error{category: ErrCategoryUser},
			wantKVs: []interface{}{"errCategory", ErrCategoryUser},
		},
		{
			name:    "*Error with explicit category and attrs, no extra kvs",
			err:     &Error{category: ErrCategoryAPIServer, attrs: []interface{}{"k1", "v1", "k2", "v2"}},
			wantKVs: []interface{}{"errCategory", ErrCategoryAPIServer, "k1", "v1", "k2", "v2"},
		},
		{
			name:    "*Error with explicit category and attrs, with extra kvs",
			err:     &Error{category: ErrCategoryUnexpected, attrs: []interface{}{"k1", "v1"}},
			kvs:     []interface{}{"k2", "v2"},
			wantKVs: []interface{}{"errCategory", ErrCategoryUnexpected, "k1", "v1", "k2", "v2"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotKVs := Args(tc.err, tc.kvs...)
			if diff := cmp.Diff(gotKVs, tc.wantKVs); diff != "" {
				t.Errorf("Args() mismatch (-got, +want):\n%s", diff)
			}
		})
	}
}

func readFromBuffer(t *testing.T, buf *bytes.Buffer) string {
	output := make([]byte, 1000)
	n, err := buf.Read(output)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from bytes buffer: %v", err)
	}
	output = output[:n]
	outputStr := string(output)
	return outputStr
}
