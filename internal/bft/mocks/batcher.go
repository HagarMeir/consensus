// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import mock "github.com/stretchr/testify/mock"

// Batcher is an autogenerated mock type for the Batcher type
type Batcher struct {
	mock.Mock
}

// BatchRemainder provides a mock function with given fields: remainder
func (_m *Batcher) BatchRemainder(remainder [][]byte) {
	_m.Called(remainder)
}

// Close provides a mock function with given fields:
func (_m *Batcher) Close() {
	_m.Called()
}

// NextBatch provides a mock function with given fields:
func (_m *Batcher) NextBatch() [][]byte {
	ret := _m.Called()

	var r0 [][]byte
	if rf, ok := ret.Get(0).(func() [][]byte); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([][]byte)
		}
	}

	return r0
}

// PopRemainder provides a mock function with given fields:
func (_m *Batcher) PopRemainder() [][]byte {
	ret := _m.Called()

	var r0 [][]byte
	if rf, ok := ret.Get(0).(func() [][]byte); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([][]byte)
		}
	}

	return r0
}

// Reset provides a mock function with given fields:
func (_m *Batcher) Reset() {
	_m.Called()
}
