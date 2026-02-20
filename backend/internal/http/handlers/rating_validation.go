package handlers

import (
	"fmt"
	"math"
)

func validateTrackerRating(rating *float64) error {
	if rating == nil {
		return nil
	}

	value := *rating
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("rating must be a valid number")
	}
	if value < 0 || value > 10 {
		return fmt.Errorf("rating must be between 0 and 10")
	}

	steps := value * 2
	if math.Abs(steps-math.Round(steps)) > 1e-9 {
		return fmt.Errorf("rating must be in 0.5 steps")
	}

	return nil
}
