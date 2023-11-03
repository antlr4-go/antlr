// Copyright (c) 2012-2022 The ANTLR Project. All rights reserved.
// Use of this file is governed by the BSD 3-clause license that
// can be found in the LICENSE.txt file in the project root.

package antlr

import "sync"

// ATNInvalidAltNumber is used to represent an ALT number that has yet to be calculated or
// which is invalid for a particular struct such as [*antlr.BaseRuleContext]
var ATNInvalidAltNumber int

// ATN represents an “[Augmented Transition Network]”, though general in ANTLR the term
// “Augmented Recursive Transition Network” though there are some descriptions of “[Recursive Transition Network]”
// in existence.
//
// ATNs represent the main networks in the system and are serialized by the code generator and support [ALL(*)].
//
// [Augmented Transition Network]: https://en.wikipedia.org/wiki/Augmented_transition_network
// [ALL(*)]: https://www.antlr.org/papers/allstar-techreport.pdf
// [Recursive Transition Network]: https://en.wikipedia.org/wiki/Recursive_transition_network
type ATN struct {

	// DecisionToState is the decision points for all rules, sub-rules, optional
	// blocks, ()+, ()*, etc. Each sub-rule/rule is a decision point, and we must track them, so we
	// can go back later and build DFA predictors for them.  This includes
	// all the rules, sub-rules, optional blocks, ()+, ()* etc...
	DecisionToState []DecisionState

	// grammarType is the ATN type and is used for deserializing ATNs from strings.
	grammarType int

	// lexerActions is referenced by action transitions in the ATN for lexer ATNs.
	lexerActions []LexerAction

	// maxTokenType is the maximum value for any symbol recognized by a transition in the ATN.
	maxTokenType int

	modeNameToStartState map[string]*TokensStartState

	modeToStartState []*TokensStartState

	// ruleToStartState maps from rule index to starting state number.
	ruleToStartState []*RuleStartState

	// ruleToStopState maps from rule index to stop state number.
	ruleToStopState []*RuleStopState

	// ruleToTokenType maps the rule index to the resulting token type for lexer
	// ATNs. For parser ATNs, it maps the rule index to the generated bypass token
	// type if ATNDeserializationOptions.isGenerateRuleBypassTransitions was
	// specified, and otherwise is nil.
	ruleToTokenType []int

	// ATNStates is a list of all states in the ATN, ordered by state number.
	//
	states []ATNState

	mu      sync.Mutex
	stateMu sync.RWMutex
	edgeMu  sync.RWMutex
}

// NewATN returns a new ATN struct representing the given grammarType and is used
// for runtime deserialization of ATNs from the code generated by the ANTLR tool
func NewATN(grammarType int, maxTokenType int) *ATN {
	return &ATN{
		grammarType:          grammarType,
		maxTokenType:         maxTokenType,
		modeNameToStartState: make(map[string]*TokensStartState),
	}
}

// NextTokensInContext computes and returns the set of valid tokens that can occur starting
// in state s. If ctx is nil, the set of tokens will not include what can follow
// the rule surrounding s. In other words, the set will be restricted to tokens
// reachable staying within the rule of s.
func (a *ATN) NextTokensInContext(s ATNState, ctx RuleContext) *IntervalSet {
	return NewLL1Analyzer(a).Look(s, nil, ctx)
}

// NextTokensNoContext computes and returns the set of valid tokens that can occur starting
// in state s and staying in same rule. [antlr.Token.EPSILON] is in set if we reach end of
// rule.
func (a *ATN) NextTokensNoContext(s ATNState) *IntervalSet {
	a.mu.Lock()
	defer a.mu.Unlock()
	iset := s.GetNextTokenWithinRule()
	if iset == nil {
		iset = a.NextTokensInContext(s, nil)
		iset.readOnly = true
		s.SetNextTokenWithinRule(iset)
	}
	return iset
}

// NextTokens computes and returns the set of valid tokens starting in state s, by
// calling either [NextTokensNoContext] (ctx == nil)  or [NextTokensInContext] (ctx != nil).
func (a *ATN) NextTokens(s ATNState, ctx RuleContext) *IntervalSet {
	if ctx == nil {
		return a.NextTokensNoContext(s)
	}

	return a.NextTokensInContext(s, ctx)
}

func (a *ATN) addState(state ATNState) {
	if state != nil {
		state.SetATN(a)
		state.SetStateNumber(len(a.states))
	}

	a.states = append(a.states, state)
}

func (a *ATN) removeState(state ATNState) {
	a.states[state.GetStateNumber()] = nil // Just free the memory; don't shift states in the slice
}

func (a *ATN) defineDecisionState(s DecisionState) int {
	a.DecisionToState = append(a.DecisionToState, s)
	s.setDecision(len(a.DecisionToState) - 1)

	return s.getDecision()
}

func (a *ATN) getDecisionState(decision int) DecisionState {
	if len(a.DecisionToState) == 0 {
		return nil
	}

	return a.DecisionToState[decision]
}

// getExpectedTokens computes the set of input symbols which could follow ATN
// state number stateNumber in the specified full parse context ctx and returns
// the set of potentially valid input symbols which could follow the specified
// state in the specified context. This method considers the complete parser
// context, but does not evaluate semantic predicates (i.e. all predicates
// encountered during the calculation are assumed true). If a path in the ATN
// exists from the starting state to the RuleStopState of the outermost context
// without Matching any symbols, Token.EOF is added to the returned set.
//
// A nil ctx defaults to ParserRuleContext.EMPTY.
//
// It panics if the ATN does not contain state stateNumber.
func (a *ATN) getExpectedTokens(stateNumber int, ctx RuleContext) *IntervalSet {
	if stateNumber < 0 || stateNumber >= len(a.states) {
		panic("Invalid state number.")
	}

	s := a.states[stateNumber]
	following := a.NextTokens(s, nil)

	if !following.Contains(TokenEpsilon) {
		return following
	}

	expected := NewIntervalSet()

	expected.addSet(following)
	expected.removeOne(TokenEpsilon)

	for ctx != nil && ctx.GetInvokingState() >= 0 && following.Contains(TokenEpsilon) {
		invokingState := a.states[ctx.GetInvokingState()]
		rt := invokingState.GetTransitions()[0]

		following = a.NextTokens(rt.(*RuleTransition).followState, nil)
		expected.addSet(following)
		expected.removeOne(TokenEpsilon)
		ctx = ctx.GetParent().(RuleContext)
	}

	if following.Contains(TokenEpsilon) {
		expected.AddOne(TokenEOF)
	}

	return expected
}

func (a *ATN) GetRuleToStartState(index int) *RuleStartState {
	return a.ruleToStartState[index]
}

func (a *ATN) GetRuleToStopState(index int) *RuleStopState {
	return a.ruleToStopState[index]
}

func (a *ATN) GetMaxTokenType() int {
	return a.maxTokenType
}
