package model

import "math"

// rotate turns (x, y) counter-clockwise by deg degrees about the origin.
func rotate(x, y, deg float64) (float64, float64) {
	if deg == 0 {
		return x, y
	}
	r := deg * math.Pi / 180
	s, c := math.Sin(r), math.Cos(r)
	return x*c - y*s, x*s + y*c
}
