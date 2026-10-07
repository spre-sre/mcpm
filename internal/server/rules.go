package server

// EvaluateRiskScore adds the weight of each set flag: flagA weighs 1, flagB 2,
// and so on up to flagL, which weighs 12.
func EvaluateRiskScore(flagA, flagB, flagC, flagD, flagE, flagF, flagG, flagH, flagI, flagJ, flagK, flagL bool) int {
	score := 0
	for index, set := range [...]bool{flagA, flagB, flagC, flagD, flagE, flagF, flagG, flagH, flagI, flagJ, flagK, flagL} {
		if set {
			score += index + 1
		}
	}
	return score
}
