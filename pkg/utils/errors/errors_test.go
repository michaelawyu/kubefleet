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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

func TestCommonUsePatterns(t *testing.T) {
	var bytesBuf bytes.Buffer
	slogHandler := slog.NewTextHandler(&bytesBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := logr.FromSlogHandler(slogHandler)
	klog.SetLogger(logger)

	rawAPIServerErr := apierrors.NewAlreadyExists(schema.GroupResource{Group: "", Resource: "namespaces"}, "work")
	categorizedAPIServerErr := NewAPIServerError(rawAPIServerErr, "failed to create namespace", false, "k1", "v1")
	wrappedErr := Wraps(categorizedAPIServerErr, "additional high-level error description", "k2", "v2", "k3", "v3")
	klog.ErrorS(wrappedErr, "additional top/controller-level error description", Args(wrappedErr)...)
	// If there needs to be kv attributes at the top level as well:
	// klog.ErrorS(wrappedErr, "additional top/controller-level error description", append(Args(wrappedErr), "k4", "v4", "k5", "v5")...)
	klog.Flush()

	output := make([]byte, 500)
	n, err := bytesBuf.Read(output)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from bytes buffer: %v", err)
	}
	output = output[:n]
	outputStr := string(output)

	// The full error output looks like the follows:
	// errors_test.go:53: time=2026-04-29T02:39:44.853+10:00 level=ERROR msg="additional top/controller-level error description" err="a/an Kubernetes API server error occurred: additional high-level error description: failed to create namespace: namespaces \"work\" already exists" k1=v1 cached=false APIServerErrReason=AlreadyExists APIServerResHTTPCode=409 k2=v2 k3=v3
	wantSubStrings := []string{
		"msg=\"additional top/controller-level error description\"",
		"err=\"a/an Kubernetes API server error occurred: additional high-level error description: failed to create namespace: namespaces \\\"work\\\" already exists\"",
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

	rawUtilityCallErr := fmt.Errorf("cannot calculate resource hash")
	categorizedUnexpectedErr := NewUnexpectedError(rawUtilityCallErr, "additional low-level error description", "k1", "v1")
	wrappedUnexpectedErr := Wraps(categorizedUnexpectedErr, "additional high-level error description", "k2", "v2")
	klog.ErrorS(wrappedUnexpectedErr, "additional top/controller-level error description", Args(wrappedUnexpectedErr)...)
	klog.Flush()

	output = make([]byte, 1000)
	n, err = bytesBuf.Read(output)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from bytes buffer: %v", err)
	}
	output = output[:n]
	outputStr = string(output)

	// The full error output looks like the follows:
	// errors_test.go:86: time=2026-04-29T02:49:04.560+10:00 level=ERROR msg="additional top/controller-level error description" err="a/an unexpected error occurred: additional high-level error description: additional low-level error description: cannot calculate resource hash" k1=v1 callers="[{Function:github.com/kubefleet-dev/kubefleet/pkg/utils/errors.TestCommonUsePatterns File:SomeFilePath Line:74} {Function:testing.tRunner File:SomeFilePath Line:1934} {Function:runtime.goexit File:SomeFilePath Line:1268}]" k2=v2
	wantSubStrings = []string{
		"msg=\"additional top/controller-level error description\"",
		"err=\"a/an unexpected error occurred: additional high-level error description: additional low-level error description: cannot calculate resource hash\"",
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
}
