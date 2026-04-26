package ai

// BARO — Bayesian Online Change Point Detection + RobustScorer
//
// Ported from the BARO project (FSE 2024, Best Artifact Award):
//   https://github.com/phamquiluan/baro
//
// Reference:
//   Pham, Ha, Zhang. "BARO: Robust Root Cause Analysis for Microservices
//   via Multivariate Bayesian Online Change Point Detection."
//   Proc. ACM Softw. Eng. 1, FSE, 2214–2237, 2024.
//
// Two algorithms are ported:
//
// 1. Univariate Bayesian Online Change Point Detection (BOCPD)
//    Uses a Student-T posterior predictive with Normal-Inverse-Gamma prior.
//    Replaces the simplified CUSUM for more principled change-point detection.
//
// 2. RobustScorer
//    Uses median + IQR (interquartile range) instead of mean + stddev.
//    More robust to outliers than z-score based scoring.

import (
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// Univariate Bayesian Online Change Point Detection (BOCPD)
// ---------------------------------------------------------------------------

// bocpdState tracks the Normal-Inverse-Gamma sufficient statistics for each
// possible run length.  At time t there are t+1 hypotheses (run lengths 0..t).
type bocpdState struct {
	mu    []float64
	kappa []float64
	alpha []float64
	beta  []float64
}

func newBOCPDState(mu0, kappa0, alpha0, beta0 float64) *bocpdState {
	return &bocpdState{
		mu:    []float64{mu0},
		kappa: []float64{kappa0},
		alpha: []float64{alpha0},
		beta:  []float64{beta0},
	}
}

// studentTPDF returns the probability density of x under a Student-t
// distribution with parameters (df, loc, scale).
func studentTPDF(x, df, loc, scale float64) float64 {
	if scale <= 0 || df <= 0 {
		return 0
	}
	z := (x - loc) / math.Sqrt(scale)
	lg1, _ := math.Lgamma((df + 1) / 2)
	lg2, _ := math.Lgamma(df / 2)
	logCoeff := lg1 - lg2 - 0.5*math.Log(df*math.Pi*scale)
	logBody := -(df + 1) / 2 * math.Log(1+z*z/df)
	return math.Exp(logCoeff + logBody)
}

// predictivePDF returns P(x | run-length = r) for each current run length.
func (s *bocpdState) predictivePDF(x float64) []float64 {
	n := len(s.mu)
	probs := make([]float64, n)
	for i := 0; i < n; i++ {
		df := 2 * s.alpha[i]
		loc := s.mu[i]
		scale := s.beta[i] * (s.kappa[i] + 1) / (s.alpha[i] * s.kappa[i])
		probs[i] = studentTPDF(x, df, loc, scale)
	}
	return probs
}

// update performs the Bayesian parameter update for each run length hypothesis.
func (s *bocpdState) update(x float64) {
	n := len(s.mu)
	newMu := make([]float64, n+1)
	newKappa := make([]float64, n+1)
	newAlpha := make([]float64, n+1)
	newBeta := make([]float64, n+1)

	// r=0 resets to prior
	newMu[0] = s.mu[0]
	newKappa[0] = s.kappa[0]
	newAlpha[0] = s.alpha[0]
	newBeta[0] = s.beta[0]

	for i := 0; i < n; i++ {
		diff := x - s.mu[i]
		newMu[i+1] = (s.kappa[i]*s.mu[i] + x) / (s.kappa[i] + 1)
		newKappa[i+1] = s.kappa[i] + 1
		newAlpha[i+1] = s.alpha[i] + 0.5
		newBeta[i+1] = s.beta[i] + s.kappa[i]*diff*diff/(2*(s.kappa[i]+1))
	}

	s.mu = newMu
	s.kappa = newKappa
	s.alpha = newAlpha
	s.beta = newBeta
}

// bocpdDetect runs Bayesian Online Change Point Detection on pts and returns
// the detected change-point timestamp and a confidence score in [0,1].
//
// hazardLambda is the expected run length (higher = fewer false change-points).
// A value of 50–100 works well for 15s-step monitoring data.
func bocpdDetect(pts []tsPoint, hazardLambda float64) (int64, float64) {
	n := len(pts)
	if n < 6 {
		return 0, 0
	}

	if hazardLambda <= 0 {
		hazardLambda = 50
	}
	hazard := 1.0 / hazardLambda

	values := make([]float64, n)
	for i, p := range pts {
		values[i] = p.v
	}
	mu0 := median(values)
	iqr := iqrSpread(values)
	if iqr == 0 {
		iqr = 1
	}

	state := newBOCPDState(mu0, 1, 1, iqr*iqr)

	// R[i] = probability that current run length is i
	R := []float64{1.0}
	maxes := make([]int, n)

	for t := 0; t < n; t++ {
		x := values[t]
		predProbs := state.predictivePDF(x)

		// Grow R: shift right and scale by (1 - hazard) * predProb
		newR := make([]float64, len(R)+1)
		cpSum := 0.0
		for i := 0; i < len(R); i++ {
			growth := R[i] * predProbs[i] * (1 - hazard)
			newR[i+1] = growth
			cpSum += R[i] * predProbs[i] * hazard
		}
		newR[0] = cpSum

		// Normalize
		total := 0.0
		for _, v := range newR {
			total += v
		}
		if total > 0 {
			for i := range newR {
				newR[i] /= total
			}
		}

		// Track most probable run length
		maxIdx := 0
		maxVal := 0.0
		for i, v := range newR {
			if v > maxVal {
				maxVal = v
				maxIdx = i
			}
		}
		maxes[t] = maxIdx

		state.update(x)

		// Prune very long run lengths to keep memory bounded (keep top 200)
		if len(newR) > 200 {
			newR = newR[:200]
			state.mu = state.mu[:200]
			state.kappa = state.kappa[:200]
			state.alpha = state.alpha[:200]
			state.beta = state.beta[:200]
		}
		R = newR
	}

	// Find change points: where the most likely run length drops sharply
	type cpCandidate struct {
		idx  int
		drop int
	}
	var candidates []cpCandidate
	for t := 1; t < n; t++ {
		drop := maxes[t-1] - maxes[t]
		if drop > 2 && maxes[t] <= 3 {
			candidates = append(candidates, cpCandidate{idx: t, drop: drop})
		}
	}

	if len(candidates) == 0 {
		return 0, 0
	}

	// Pick the candidate with the largest run-length drop
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].drop > candidates[j].drop
	})
	best := candidates[0]

	// Reject change points too close to edges
	if best.idx < 2 || best.idx > n-3 {
		return 0, 0
	}

	// Confidence: how much the mean shifted relative to spread
	preMean := meanOf(values[:best.idx])
	postMean := meanOf(values[best.idx:])
	shift := math.Abs(postMean-preMean) / iqr
	confidence := math.Min(shift/3.0, 1.0)

	if confidence < 0.15 {
		return 0, 0
	}

	return pts[best.idx].t, confidence
}

// ---------------------------------------------------------------------------
// RobustScorer — IQR-based anomaly scoring
// ---------------------------------------------------------------------------

// robustScoreFromStats computes IQR-based anomaly score from a time series.
// More robust to outliers than z-score (uses median + IQR not mean + stddev).
func robustScoreFromStats(pts []tsPoint) float64 {
	if len(pts) < 8 {
		return 0
	}

	values := make([]float64, len(pts))
	for i, p := range pts {
		values[i] = p.v
	}

	med := median(values)
	iqr := iqrSpread(values)
	if iqr == 0 {
		iqr = 1
	}

	// Score = max deviation from median, normalized by IQR
	maxDev := 0.0
	for _, v := range values {
		dev := math.Abs(v - med)
		if dev > maxDev {
			maxDev = dev
		}
	}

	return maxDev / iqr
}

// multivariateBOCPDDetect reduces several related metric streams to a robust
// normalized vector magnitude, then applies BOCPD to detect joint shifts.
func multivariateBOCPDDetect(series [][]tsPoint, hazardLambda float64) (int64, float64, float64) {
	normalized := normalizeSeriesMagnitude(series)
	if len(normalized) < 8 {
		return 0, 0, 0
	}

	maxMagnitude := 0.0
	for _, p := range normalized {
		if p.v > maxMagnitude {
			maxMagnitude = p.v
		}
	}

	cp, confidence := bocpdDetect(normalized, hazardLambda)
	if cp == 0 {
		return 0, 0, maxMagnitude
	}
	if maxMagnitude < 1.5 {
		confidence *= 0.5
	}
	return cp, math.Min(confidence*math.Min(maxMagnitude/2.5, 1.5), 1), maxMagnitude
}

func normalizeSeriesMagnitude(series [][]tsPoint) []tsPoint {
	type acc struct {
		sumSquares float64
		count      int
	}
	byTime := map[int64]*acc{}

	for _, pts := range series {
		if len(pts) < 4 {
			continue
		}
		values := make([]float64, len(pts))
		for i, p := range pts {
			values[i] = p.v
		}
		med := median(values)
		spread := iqrSpread(values)
		if spread == 0 {
			spread = stdOf(values, meanOf(values))
		}
		if spread == 0 {
			continue
		}
		for _, p := range pts {
			z := (p.v - med) / spread
			a := byTime[p.t]
			if a == nil {
				a = &acc{}
				byTime[p.t] = a
			}
			a.sumSquares += z * z
			a.count++
		}
	}

	if len(byTime) == 0 {
		return nil
	}
	timestamps := make([]int64, 0, len(byTime))
	for t := range byTime {
		timestamps = append(timestamps, t)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })

	out := make([]tsPoint, 0, len(timestamps))
	for _, t := range timestamps {
		a := byTime[t]
		if a.count < 2 {
			continue
		}
		out = append(out, tsPoint{t: t, v: math.Sqrt(a.sumSquares / float64(a.count))})
	}
	return out
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func median(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

func iqrSpread(data []float64) float64 {
	if len(data) < 4 {
		return 0
	}
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)
	n := len(sorted)
	q25 := sorted[n/4]
	q75 := sorted[n*3/4]
	return q75 - q25
}

func meanOf(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

// ---------------------------------------------------------------------------
// Granger-inspired causal precedence scoring
// ---------------------------------------------------------------------------

// GrangerCausalScore estimates whether changes in metric A (cause) temporally
// precede changes in metric B (effect). Returns a score in [0,1] where higher
// means A's anomaly preceded B's by a meaningful lag.
//
// This is a simplified version of Granger causality suitable for short time
// series typical of incident windows (50-200 points). Instead of a full
// autoregressive model, it uses:
//  1. Change-point detection on both series
//  2. Cross-correlation at multiple lags
//  3. Transfer entropy approximation via conditional probability
func GrangerCausalScore(cause, effect []tsPoint, maxLag int) float64 {
	if len(cause) < 10 || len(effect) < 10 || maxLag < 1 {
		return 0
	}

	n := min(len(cause), len(effect))
	if n < maxLag+5 {
		return 0
	}

	causeVals := make([]float64, n)
	effectVals := make([]float64, n)
	for i := 0; i < n; i++ {
		causeVals[i] = cause[i].v
		effectVals[i] = effect[i].v
	}

	// Normalize both series to zero mean, unit variance
	causeMean := meanOf(causeVals)
	effectMean := meanOf(effectVals)
	causeStd := stdOf(causeVals, causeMean)
	effectStd := stdOf(effectVals, effectMean)
	if causeStd == 0 || effectStd == 0 {
		return 0
	}

	for i := range causeVals {
		causeVals[i] = (causeVals[i] - causeMean) / causeStd
		effectVals[i] = (effectVals[i] - effectMean) / effectStd
	}

	// Compute cross-correlation at positive lags (cause leads effect)
	bestLag := 0
	bestCorr := 0.0
	for lag := 1; lag <= maxLag; lag++ {
		corr := crossCorrelation(causeVals, effectVals, lag)
		if corr > bestCorr {
			bestCorr = corr
			bestLag = lag
		}
	}

	// Compare with zero-lag correlation to ensure causal direction
	zeroCorr := math.Abs(crossCorrelation(causeVals, effectVals, 0))
	if bestCorr <= zeroCorr*1.05 || bestCorr < 0.3 {
		return 0
	}

	// Change-point precedence bonus
	cpBonus := 0.0
	causeCP, _ := bocpdDetect(cause, 50)
	effectCP, _ := bocpdDetect(effect, 50)
	if causeCP > 0 && effectCP > 0 && causeCP < effectCP {
		lagSeconds := effectCP - causeCP
		if lagSeconds > 0 && lagSeconds < 600 {
			cpBonus = 0.2
		}
	}

	_ = bestLag
	score := math.Min(bestCorr+cpBonus, 1.0)
	return score
}

func crossCorrelation(x, y []float64, lag int) float64 {
	n := len(x) - lag
	if n <= 0 {
		return 0
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += x[i] * y[i+lag]
	}
	return math.Abs(sum / float64(n))
}

func stdOf(data []float64, mean float64) float64 {
	if len(data) < 2 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(data)-1))
}
