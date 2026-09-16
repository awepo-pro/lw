// Package uitest is the test harness contract §6 gives the 003 screens and
// the conformance gate (s1-harness-gate.md T05): a staged fixture vault, a
// headless driver that reproduces what tea.Program would deliver, key and
// screen helpers measured in cells, and a scripted agent.Agent.
//
// It is a non-test package so external test packages can import it, and it
// must never be imported by production code: the screens' _test.go files and
// internal/ui/conformance are its only users. Its public fixture
// (testdata/vault, a small bread-baking wiki written for this repo) is the
// vault behind PublicVault; private vaults reach the harness only through
// CopyVault.
package uitest
