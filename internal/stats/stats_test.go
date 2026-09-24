package stats

import (
	"math"
	"math/rand"
	"testing"
)

func almost(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestPassAtKKnownValues(t *testing.T) {
	// n=1 trial, k=1: pass@1 is just whether it passed.
	if v := PassAtK(1, 1, 1); v != 1 {
		t.Fatalf("PassAtK(1,1,1) = %v, want 1", v)
	}
	if v := PassAtK(1, 0, 1); v != 0 {
		t.Fatalf("PassAtK(1,0,1) = %v, want 0", v)
	}
	// n=10, c=0: never passes, regardless of k.
	if v := PassAtK(10, 0, 5); v != 0 {
		t.Fatalf("PassAtK(10,0,5) = %v, want 0", v)
	}
	// n=10, c=10: always passes.
	if v := PassAtK(10, 10, 3); v != 1 {
		t.Fatalf("PassAtK(10,10,3) = %v, want 1", v)
	}
	// n=5, c=1, k=1: probability a single random trial passes = c/n = 0.2.
	if v := PassAtK(5, 1, 1); !almost(v, 0.2, 1e-9) {
		t.Fatalf("PassAtK(5,1,1) = %v, want 0.2", v)
	}
	// n=4, c=2, k=2: 1 - C(2,2)/C(4,2) = 1 - 1/6 = 0.8333...
	if v := PassAtK(4, 2, 2); !almost(v, 1-1.0/6, 1e-9) {
		t.Fatalf("PassAtK(4,2,2) = %v, want %v", v, 1-1.0/6)
	}
	// n=5, c=3, k=2: 1 - C(2,2)/C(5,2) = 1 - 1/10 = 0.9
	if v := PassAtK(5, 3, 2); !almost(v, 0.9, 1e-9) {
		t.Fatalf("PassAtK(5,3,2) = %v, want 0.9", v)
	}
	// fewer failures than k: guaranteed a pass in the subset.
	if v := PassAtK(5, 4, 2); v != 1 {
		t.Fatalf("PassAtK(5,4,2) = %v, want 1", v)
	}
}

func TestPassAtKMean(t *testing.T) {
	ts := []TaskTrials{{TaskID: "a", N: 5, C: 1}, {TaskID: "b", N: 5, C: 5}}
	got := PassAtKMean(ts, 1)
	want := (0.2 + 1.0) / 2
	if !almost(got, want, 1e-9) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestVarianceAndFlaky(t *testing.T) {
	allPass := TaskTrials{N: 5, C: 5}
	if allPass.Variance() != 0 || allPass.Flaky() {
		t.Fatalf("always-pass task should have 0 variance and not be flaky: %+v", allPass)
	}
	allFail := TaskTrials{N: 5, C: 0}
	if allFail.Variance() != 0 || allFail.Flaky() {
		t.Fatalf("always-fail task should have 0 variance and not be flaky: %+v", allFail)
	}
	half := TaskTrials{N: 4, C: 2}
	if !almost(half.Variance(), 0.25, 1e-9) || !half.Flaky() {
		t.Fatalf("half-pass task: variance=%v flaky=%v, want 0.25 true", half.Variance(), half.Flaky())
	}
}

func TestWilsonIntervalKnownCase(t *testing.T) {
	// Textbook check: 0 successes out of 10, 95% Wilson interval has lo=0
	// and a small positive upper bound (~0.2775), unlike the normal
	// approximation which degenerates to [0,0].
	lo, hi := WilsonInterval(0, 10, 0.95)
	if lo != 0 {
		t.Fatalf("lo = %v, want 0", lo)
	}
	if !almost(hi, 0.2775, 0.001) {
		t.Fatalf("hi = %v, want ~0.2775", hi)
	}
	// 5/10 is symmetric around 0.5.
	lo, hi = WilsonInterval(5, 10, 0.95)
	if !almost((lo+hi)/2, 0.5, 0.01) {
		t.Fatalf("midpoint = %v, want ~0.5", (lo+hi)/2)
	}
	if lo <= 0 || hi >= 1 || lo >= hi {
		t.Fatalf("bad interval [%v,%v]", lo, hi)
	}
	// n=0 is defined as [0,0], not NaN/panic.
	if lo, hi := WilsonInterval(0, 0, 0.95); lo != 0 || hi != 0 {
		t.Fatalf("n=0 case: got [%v,%v]", lo, hi)
	}
}

func TestBootstrapCIContainsTrueMeanForConstantData(t *testing.T) {
	xs := []float64{1, 1, 1, 1, 1}
	rng := rand.New(rand.NewSource(1))
	lo, hi := BootstrapCI(xs, 500, 0.95, rng)
	if lo != 1 || hi != 1 {
		t.Fatalf("constant data should give a degenerate [1,1] interval, got [%v,%v]", lo, hi)
	}
}

func TestBootstrapCIReasonableForMixedData(t *testing.T) {
	xs := []float64{0, 0, 0, 1, 1, 1, 1, 1, 1, 1} // mean 0.7
	rng := rand.New(rand.NewSource(42))
	lo, hi := BootstrapCI(xs, 2000, 0.95, rng)
	if lo < 0 || hi > 1 || lo >= hi {
		t.Fatalf("bad interval [%v,%v]", lo, hi)
	}
	if lo > 0.7 || hi < 0.7 {
		t.Fatalf("interval [%v,%v] should contain the sample mean 0.7", lo, hi)
	}
}

func TestMcNemarKnownValues(t *testing.T) {
	// No discordant pairs: not significant, p=1.
	if chi2, p := McNemar(0, 0); chi2 != 0 || p != 1 {
		t.Fatalf("McNemar(0,0) = %v,%v want 0,1", chi2, p)
	}
	// b=c: no evidence of difference regardless of magnitude.
	if chi2, p := McNemar(10, 10); chi2 != 0 || !almost(p, 1, 1e-9) {
		t.Fatalf("McNemar(10,10) = %v,%v want 0,~1", chi2, p)
	}
	// Strongly asymmetric: b=20,c=2 should be highly significant (p<0.001).
	if chi2, p := McNemar(20, 2); chi2 <= 0 || p >= 0.001 {
		t.Fatalf("McNemar(20,2) = %v,%v want large chi2, tiny p", chi2, p)
	}
	// Classic textbook example: b=9, c=3 -> chi2=(|9-3|-1)^2/12=25/12=2.0833,
	// which is just below the 5% critical value (3.841), so not significant.
	chi2, p := McNemar(9, 3)
	if !almost(chi2, 25.0/12, 1e-9) {
		t.Fatalf("chi2 = %v want %v", chi2, 25.0/12)
	}
	if p <= 0.05 {
		t.Fatalf("p = %v, expected > 0.05 for chi2=2.08", p)
	}
}

func TestPairedBootstrapDiffAgreesWhenIdentical(t *testing.T) {
	a := []float64{1, 0, 1, 1, 0, 1, 1, 1}
	rng := rand.New(rand.NewSource(7))
	est, lo, hi := PairedBootstrapDiff(a, a, 1000, 0.95, rng)
	if est != 0 || lo != 0 || hi != 0 {
		t.Fatalf("identical paired samples should give a zero diff, got est=%v [%v,%v]", est, lo, hi)
	}
	if Significant(lo, hi) {
		t.Fatal("identical samples must not be flagged significant")
	}
}

func TestPairedBootstrapDiffDetectsClearDifference(t *testing.T) {
	a := make([]float64, 30)
	b := make([]float64, 30)
	for i := range a {
		if i < 27 {
			a[i] = 1 // agent A passes 27/30
		}
		if i < 6 {
			b[i] = 1 // agent B passes 6/30
		}
	}
	rng := rand.New(rand.NewSource(3))
	est, lo, hi := PairedBootstrapDiff(a, b, 2000, 0.95, rng)
	if !almost(est, 0.7, 1e-9) {
		t.Fatalf("estimate = %v, want 0.7", est)
	}
	if !Significant(lo, hi) {
		t.Fatalf("a clear 27/30 vs 6/30 difference should be significant, got [%v,%v]", lo, hi)
	}
}

func TestSignificant(t *testing.T) {
	if Significant(-0.1, 0.1) {
		t.Fatal("CI spanning zero must not be significant")
	}
	if !Significant(0.01, 0.2) {
		t.Fatal("CI entirely above zero must be significant")
	}
	if !Significant(-0.2, -0.01) {
		t.Fatal("CI entirely below zero must be significant")
	}
}
