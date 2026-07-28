package interpreter_test

import (
	"testing"

	"github.com/DTrader-store/formula-go/engine"
	"github.com/DTrader-store/formula-go/interpreter"
	"github.com/DTrader-store/formula-go/types"
)

func TestInterpreterRegisterFunctionMakesFunctionExecutable(t *testing.T) {
	formulaEngine := engine.NewFormulaEngine()
	program, err := formulaEngine.Compile("RESULT := DOUBLE(21)")
	if err != nil {
		t.Fatalf("compile program: %v", err)
	}
	interp := interpreter.NewInterpreter(nil)
	interp.RegisterFunction("DOUBLE", doubleFunction)

	result, err := interp.Execute(program)
	if err != nil {
		t.Fatalf("execute program: %v", err)
	}

	if value, ok := result.Variables["RESULT"]; !ok || value != 42 {
		t.Fatalf("RESULT = %v, %t; want 42, true", value, ok)
	}
}

func TestFormulaEngineRegisterFunctionMakesFunctionExecutable(t *testing.T) {
	formulaEngine := engine.NewFormulaEngine()
	formulaEngine.RegisterFunction("DOUBLE", doubleFunction)

	result, err := formulaEngine.Run("RESULT := DOUBLE(21)", nil)
	if err != nil {
		t.Fatalf("run formula: %v", err)
	}

	if value, ok := result.Variables["RESULT"]; !ok || value != 42 {
		t.Fatalf("RESULT = %v, %t; want 42, true", value, ok)
	}
}

func TestCustomRegistryCanBeInjected(t *testing.T) {
	registry := interpreter.NewFunctionRegistry()
	registry.Register("DOUBLE", doubleFunction)
	formulaEngine := engine.NewFormulaEngineWithRegistry(registry)

	result, err := formulaEngine.Run("RESULT := DOUBLE(21)", nil)
	if err != nil {
		t.Fatalf("run formula: %v", err)
	}

	if value, ok := result.Variables["RESULT"]; !ok || value != 42 {
		t.Fatalf("RESULT = %v, %t; want 42, true", value, ok)
	}
}

func TestInterpreterExecuteResetsStateBetweenPrograms(t *testing.T) {
	formulaEngine := engine.NewFormulaEngine()
	firstProgram, err := formulaEngine.Compile("OLD := 1\nFIRST: 2")
	if err != nil {
		t.Fatalf("compile first program: %v", err)
	}
	secondProgram, err := formulaEngine.Compile("NEW := 3")
	if err != nil {
		t.Fatalf("compile second program: %v", err)
	}

	interp := interpreter.NewInterpreter(nil)
	if _, err := interp.Execute(firstProgram); err != nil {
		t.Fatalf("execute first program: %v", err)
	}

	result, err := interp.Execute(secondProgram)
	if err != nil {
		t.Fatalf("execute second program: %v", err)
	}

	if len(result.Outputs) != 0 {
		t.Fatalf("second result contains outputs from first program: %+v", result.Outputs)
	}
	if value, ok := result.Variables["NEW"]; !ok || value != 3 {
		t.Fatalf("second result NEW = %v, %t; want 3, true", value, ok)
	}
	if _, ok := result.Variables["OLD"]; ok {
		t.Fatal("second result contains variable OLD from first program")
	}
}

func TestInterpreterRejectsNilProgram(t *testing.T) {
	interp := interpreter.NewInterpreter(nil)
	if _, err := interp.Execute(nil); err == nil {
		t.Fatal("Execute(nil) error = nil, want an error")
	}
}

func doubleFunction(args []*interpreter.Value, _ []*types.MarketData) (*interpreter.Value, error) {
	return interpreter.NewSingleValue(args[0].Single * 2), nil
}
