package cbor

// Compile-time assertions that every kind encoder and every decoder still
// answers its interface. They matter here because the plans reach most of
// them through a map keyed by reflect.Kind: a method renamed or dropped would
// not fail to compile at the map, and the plan for that kind would be missing
// a method only at run time.
var (
	_ kindEncoder = boolEncoder{}
	_ kindEncoder = intEncoder{}
	_ kindEncoder = uintEncoder{}
	_ kindEncoder = floatEncoder{}
	_ kindEncoder = stringEncoder{}
	_ kindEncoder = timeEncoder{}
	_ kindEncoder = bigIntEncoder{}
	_ kindEncoder = cborMarshalerEncoder{}
	_ kindEncoder = binaryMarshalerEncoder{}
	_ kindEncoder = pointerEncoder{}
	_ kindEncoder = interfaceEncoder{}
	_ kindEncoder = byteSequenceEncoder{}
	_ kindEncoder = sequenceEncoder{}
	_ kindEncoder = mapEncoder{}
	_ kindEncoder = structEncoder{}
	_ kindEncoder = structArrayEncoder{}
	_ kindEncoder = refusedEncoder{}
	_ decoder     = boolDecoder{}
	_ decoder     = intDecoder{}
	_ decoder     = uintDecoder{}
	_ decoder     = floatDecoder{}
	_ decoder     = stringDecoder{}
	_ decoder     = timeDecoder{}
	_ decoder     = bigIntDecoder{}
	_ decoder     = rawDecoder{}
	_ decoder     = refusedDecoder{}
	_ decoder     = pointerDecoder{}
	_ decoder     = emptyInterfaceDecoder{}
	_ decoder     = interfaceDecoder{}
	_ decoder     = byteSliceDecoder{}
	_ decoder     = byteArrayDecoder{}
	_ decoder     = sliceDecoder{}
	_ decoder     = arrayDecoder{}
	_ decoder     = mapDecoder{}
	_ decoder     = structDecoder{}
)
