package main

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestAreaProgressExactPrefixes(t *testing.T) {
	for _, dims := range []int{2, 3} {
		for _, ball := range []bool{false, true} {
			request := areaPageRequest{dims: dims, first: [3]int64{-1, -1, -1}, last: [3]int64{1, 1, 1}}
			if ball {
				radius := uint64(2)
				request.first, request.around, request.radius = [3]int64{}, true, radius
			}
			var points [][3]int64
			for x := int64(-2); x <= 2; x++ {
				for y := int64(-2); y <= 2; y++ {
					for z := int64(-2); z <= 2; z++ {
						if dims == 2 && z != 0 {
							continue
						}
						inside := x >= -1 && x <= 1 && y >= -1 && y <= 1 && (dims == 2 || z >= -1 && z <= 1)
						if ball {
							inside = x*x+y*y+z*z <= 4
						}
						if inside {
							points = append(points, [3]int64{x, y, z})
						}
					}
				}
			}
			for start := 0; start <= len(points); start++ {
				request.after = nil
				if start != 0 {
					request.after = &points[start-1]
				}
				for limit := 1; limit <= len(points)+1; limit++ {
					for end := start; end <= len(points); end++ {
						var cursor *[3]int64
						if end != len(points) {
							cursor = &points[end]
						}
						count := end - start + 1
						if cursor == nil {
							count--
						}
						want := count <= limit
						if cursor != nil && end == len(points)-1 {
							want = false
						}
						got, err := request.validProgressChecked(cursor, uint64(limit), nil)
						if err != nil || got != want {
							t.Fatalf("dims=%d ball=%t start=%d end=%d limit=%d: %t %v, want %t", dims, ball, start, end, limit, got, err, want)
						}
					}
				}
			}
		}
	}
}

func TestAreaBoxProgressHasConstantWork(t *testing.T) {
	request := areaPageRequest{dims: 3, first: [3]int64{math.MinInt64, math.MinInt64, math.MinInt64}, last: [3]int64{math.MaxInt64, math.MaxInt64, math.MaxInt64}}
	check := func(cursor *[3]int64, limit uint64, want bool) {
		t.Helper()
		calls := 0
		got, err := request.validProgressChecked(cursor, limit, func() error {
			calls++
			if calls > 2 {
				return errors.New("rectangle validation enumerated cells")
			}
			return nil
		})
		if err != nil || got != want || calls != 2 {
			t.Fatal(got, err, calls, want)
		}
	}
	check(nil, math.MaxUint64, false)
	request.after = &[3]int64{math.MaxInt64, math.MaxInt64, math.MaxInt64 - 1}
	check(nil, 1, true)
	request.after = &[3]int64{math.MaxInt64, math.MaxInt64, math.MaxInt64 - 2}
	check(&[3]int64{math.MaxInt64, math.MaxInt64, math.MaxInt64 - 1}, 1, true)
	request = areaPageRequest{dims: 2, first: [3]int64{math.MinInt64, 0}, last: [3]int64{math.MaxInt64, 0}}
	check(nil, math.MaxUint64, false)
	request.after = &[3]int64{math.MinInt64, 0}
	check(nil, math.MaxUint64, true)
}

func TestAreaBallProgressChecksWholeRowsAndDeadline(t *testing.T) {
	radius := uint64(2)
	for _, dims := range []int{2, 3} {
		request := areaPageRequest{dims: dims, around: true, radius: radius}
		rows, cells := 5, 13
		if dims == 3 {
			rows, cells = 13, 33
		}
		calls := 0
		valid, err := request.validProgressChecked(nil, uint64(cells), func() error { calls++; return nil })
		if err != nil || !valid || calls != rows+2 {
			t.Fatal(dims, valid, err, calls)
		}
		calls = 0
		_, err = request.validProgressChecked(nil, uint64(cells), func() error {
			calls++
			if calls == 3 {
				return context.DeadlineExceeded
			}
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || calls != 3 {
			t.Fatal(err, calls)
		}
	}
}

func TestAreaBallProgressInterruptsBetweenRows(t *testing.T) {
	request := areaPageRequest{dims: 3, around: true, radius: 2}
	calls := 0
	valid, err := request.validProgressChecked(nil, 33, func() error {
		calls++
		if calls == 3 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if valid || !errors.Is(err, context.DeadlineExceeded) || calls != 3 {
		t.Fatal(valid, err, calls)
	}
}
