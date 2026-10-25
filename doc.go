// Package gasometer is the vocabulary of a service that measures how much EVM fee estimators overpay to get a
// transaction included, and how often their bids land: its types, the ports to the outside world and its errors. The
// fee model is in fee, the predictors in predict, scoring in score and the per-network poll in poll. It depends only
// on the standard library.
package gasometer
