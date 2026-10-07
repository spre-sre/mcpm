package server

func EvaluateRiskScore(flagA, flagB, flagC, flagD, flagE, flagF, flagG, flagH, flagI, flagJ, flagK, flagL bool) int {
	score := 0
	if flagA {
		score += 1
	}
	if flagB {
		score += 2
	}
	if flagC {
		score += 3
	}
	if flagD {
		score += 4
	}
	if flagE {
		score += 5
	}
	if flagF {
		score += 6
	}
	if flagG {
		score += 7
	}
	if flagH {
		score += 8
	}
	if flagI {
		score += 9
	}
	if flagJ {
		score += 10
	}
	if flagK {
		score += 11
	}
	if flagL {
		score += 12
	}
	return score
}
