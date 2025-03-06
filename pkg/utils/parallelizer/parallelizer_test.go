/*
Copyright 2025 The KubeFleet Authors.

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

package parallelizer

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestParallelizer tests the basic ops of a Parallelizer.
func TestParallelizer(t *testing.T) {
	p := NewParallelizer(DefaultNumOfWorkers)
	nums := []int{1, 2, 3, 4, 5, 6, 7, 8}

	var sum int32
	doWork := func(pieces int) {
		num := nums[pieces]
		atomic.AddInt32(&sum, int32(num))
	}

	ctx := context.Background()
	p.ParallelizeUntil(ctx, len(nums), doWork, "test")
	if sum != 36 {
		t.Errorf("sum of nums, want %d, got %d", 36, sum)
	}
}
