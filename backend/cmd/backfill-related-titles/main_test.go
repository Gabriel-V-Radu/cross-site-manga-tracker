package main

import (
	"reflect"
	"testing"
)

func TestBuildStoredRelatedTitlesExcludesMainTitles(t *testing.T) {
	got := buildStoredRelatedTitles(
		"The Devil Butler",
		"The Devil Butler",
		[]string{"The Devil Butler", "Demonic Emperor", "Mo Huang Da Guan Jia"},
	)

	want := []string{"Demonic Emperor", "Mo Huang Da Guan Jia"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected related titles: got %v want %v", got, want)
	}
}

func TestBuildStoredRelatedTitlesExcludesTrackerAndResolvedMainTitles(t *testing.T) {
	got := buildStoredRelatedTitles(
		"Solo Leveling",
		"Solo Leveling: Ragnarok",
		[]string{"Solo Leveling", "Solo Leveling: Ragnarok", "Leveling Up Alone"},
	)

	want := []string{"Leveling Up Alone"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected related titles: got %v want %v", got, want)
	}
}

func TestBuildStoredRelatedTitlesFiltersNonEnglishAndEmptyResult(t *testing.T) {
	got := buildStoredRelatedTitles(
		"Nano Machine",
		"Nano Machine",
		[]string{"Nano Machine", "나노마신"},
	)

	if got != nil {
		t.Fatalf("expected nil related titles, got %v", got)
	}
}
