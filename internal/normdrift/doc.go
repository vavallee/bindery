// Package normdrift holds no production code. It exists so one test file can
// import every package that owns a normalization alphabet at once, and assert
// the relationships BETWEEN them.
//
// Every bug in the #940 / #871 / #1608 / #1642-#1647 family is the same shape:
// two sides of a string comparison reduced through different alphabets, so the
// match is silently always-false. Each one was found by hand, months apart,
// after users reported the symptom. The properties asserted next door are the
// cheapest thing that would have caught most of them automatically.
//
// The normalizers themselves are documented in internal/textutil/fold.go.
package normdrift
