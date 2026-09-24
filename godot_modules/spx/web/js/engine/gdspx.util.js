/*
 * SPX Web FFI 的参数转换与 WASM 内存工具。
 *
 * JavaScript、Go WASM 和 Godot C++ 虽然运行在同一浏览器里，却不能直接共享
 * String、Vector2、Color、Array 等高级对象。它们真正能共同访问的是 Godot
 * WASM 的“线性内存”。本文件负责分配内存、写入参数、读出结果和释放内存。
 *
 * 初学者阅读提示：
 * - Module['HEAPU8']、HEAPU32、HEAPF32 是同一段 WASM 内存的不同类型视图。
 * - ptr 是内存地址（数字），不是 JavaScript 对象。
 * - new TextEncoder/TextDecoder 负责 UTF-8 字符串与字节之间的转换。
 * - 默认参数 out = {} 表示调用者不传 out 时自动创建空对象。
 * - Object.freeze(...) 让对象结构不可再修改，避免内存描述信息被篡改。
 */

const GDSPX_UTF8_ENCODER = new TextEncoder();
const GDSPX_UTF8_DECODER = new TextDecoder("utf-8");
const GDSPX_MAX_STRING_BYTES = 256 * 1024 * 1024;

// -----------------------------------------------------------------------------
// 标量与对象 ID 转换
// -----------------------------------------------------------------------------

// Go 的 syscall/js 把 int64 拆成两个精确的 uint32：low 和 high。只有真正调用
// Godot WASM 前，才在 JavaScript 中把两部分重新拼成一个 64 位 BigInt。
function GdInt64FromParts(low, high) {
    return BigInt.asIntN(64, (BigInt(high >>> 0) << 32n) | BigInt(low >>> 0));
}

function ToJsBool(ptr) {
    const HEAPU8 = Module['HEAPU8'];
    const boolValue = HEAPU8[ptr];
    return boolValue !== 0;
}

function AllocGdBool() {
    return Module['_gdspx_alloc_bool']();
}

function FreeGdBool(ptr) {
    Module['_gdspx_free_bool'](ptr);
}

function ToJsObj(ptr, result) {
    return ToJsInt(ptr, result);
}

function AllocGdObj() {
    return Module['_gdspx_alloc_obj']();
}

function FreeGdObj(ptr) {
    Module['_gdspx_free_obj'](ptr);
}

// WASM 标量指针按 4 字节对齐，所以 ptr >>> 2 可把“字节地址”换算成 HEAPU32
// 的数组下标。传入 result 时会复用该对象；下一次写入前，调用者必须读完或复制。
function ToJsInt(ptr, result = {}) {
    const words = Module['HEAPU32'];
    const index = ptr >>> 2;
    result['low'] = words[index];
    result['high'] = words[index + 1];
    return result;
}

function AllocGdInt() {
    return Module['_gdspx_alloc_int']();
}

function FreeGdInt(ptr) {
    Module['_gdspx_free_int'](ptr);
}

// -----------------------------------------------------------------------------
// 字符串与 Vector、Color、Rect2 等结构体转换
// -----------------------------------------------------------------------------

function ToJsFloat(ptr) {
    const HEAPF32 = Module['HEAPF32'];
    const floatIndex = ptr / 4;
    const floatValue = HEAPF32[floatIndex];
    return floatValue;
}

function AllocGdFloat() {
    return Module['_gdspx_alloc_float']();
}

function FreeGdFloat(ptr) {
    Module['_gdspx_free_float'](ptr);
}

function ToGdString(str) {
	// _cmalloc/_cfree 是 C 内存分配函数。先把 JS 字符串编码为 UTF-8 字节，写入
	// WASM 内存，再让 _gdspx_new_string 构造 Godot 能理解的字符串包装对象。
    const malloc = Module['_cmalloc'];
    const free = Module['_cfree'];
    const stringBytes = GDSPX_UTF8_ENCODER.encode(str);
    const allocationSize = stringBytes.length + 1;
    if (!Number.isSafeInteger(allocationSize) || allocationSize > GDSPX_MAX_STRING_BYTES ||
            typeof malloc !== 'function' || typeof free !== 'function') {
        throw new Error("String is too large or the Wasm allocator is unavailable");
    }
    const ptr = malloc(allocationSize);
    if (!Number.isSafeInteger(ptr) || ptr <= 0 || !IsHeapRange(ptr, allocationSize)) {
        throw new Error("Failed to allocate a Wasm string buffer");
    }
    Module['HEAPU8'].set(stringBytes, ptr);
    Module['HEAPU8'][ptr + stringBytes.length] = 0;
    const gdstrPtr = Module['_gdspx_new_string'](ptr, stringBytes.length);

    //调用完后释放内存
    free(ptr);
    if (!Number.isSafeInteger(gdstrPtr) || gdstrPtr <= 0) {
        throw new Error("Failed to allocate a GdString wrapper");
    }
    return gdstrPtr;
}

function ToJsString(gdstrPtr) {
	// 先向 C++ 查询 UTF-8 数据地址和长度，再从 HEAPU8 截取相应字节并解码。
    if (!gdstrPtr || typeof Module['_gdspx_get_string_len'] !== 'function' ||
            typeof Module['_gdspx_get_string'] !== 'function') {
        return '';
    }
    const length = Module['_gdspx_get_string_len'](gdstrPtr);
    const ptr = Module['_gdspx_get_string'](gdstrPtr);
    if (!Number.isSafeInteger(length) || length < 0 || length > GDSPX_MAX_ARRAY_BYTES ||
            !Number.isSafeInteger(ptr) || ptr <= 0 || !IsHeapRange(ptr, length)) {
        return '';
    }
    const stringBytes = Module['HEAPU8'].subarray(ptr, ptr + length);
    return GDSPX_UTF8_DECODER.decode(stringBytes);
}

function AllocGdString() {
    return Module['_gdspx_alloc_string']();
}

function FreeGdString(ptr) {
    Module['_gdspx_free_string'](ptr);
}

function ToGdVec2(vec) {
    return Module['_gdspx_new_vec2'](vec['x'], vec['y']);
}

function ToJsVec2(ptr, out = {}) {
    const HEAPF32 = Module['HEAPF32'];
    const floatIndex = ptr / 4;
    out['x'] = HEAPF32[floatIndex];
    out['y'] = HEAPF32[floatIndex + 1];
    return out;
}

function AllocGdVec2() {
    return Module['_gdspx_alloc_vec2']();
}

function FreeGdVec2(ptr) {
    Module['_gdspx_free_vec2'](ptr);
}

function ToGdVec3(vec) {
    return Module['_gdspx_new_vec3'](vec['x'], vec['y'], vec['z']);
}

function ToJsVec3(ptr, out = {}) {
    const HEAPF32 = Module['HEAPF32'];
    const floatIndex = ptr / 4;
    out['x'] = HEAPF32[floatIndex];
    out['y'] = HEAPF32[floatIndex + 1];
    out['z'] = HEAPF32[floatIndex + 2];
    return out;
}

function AllocGdVec3() {
    return Module['_gdspx_alloc_vec3']();
}

function FreeGdVec3(ptr) {
    Module['_gdspx_free_vec3'](ptr);
}

function ToGdVec4(vec) {
    return Module['_gdspx_new_vec4'](vec['x'], vec['y'], vec['z'], vec['w']);
}

function ToJsVec4(ptr, out = {}) {
    const HEAPF32 = Module['HEAPF32'];
    const floatIndex = ptr / 4;
    out['x'] = HEAPF32[floatIndex];
    out['y'] = HEAPF32[floatIndex + 1];
    out['z'] = HEAPF32[floatIndex + 2];
    out['w'] = HEAPF32[floatIndex + 3];
    return out;
}

function AllocGdVec4() {
    return Module['_gdspx_alloc_vec4']();
}

function FreeGdVec4(ptr) {
    Module['_gdspx_free_vec4'](ptr);
}

function ToGdColor(color) {
    return Module['_gdspx_new_color'](color['r'], color['g'], color['b'], color['a']);
}

function ToJsColor(ptr, out = {}) {
    const HEAPF32 = Module['HEAPF32'];
    const floatIndex = ptr / 4;
    out['r'] = HEAPF32[floatIndex];
    out['g'] = HEAPF32[floatIndex + 1];
    out['b'] = HEAPF32[floatIndex + 2];
    out['a'] = HEAPF32[floatIndex + 3];
    return out;
}

function AllocGdColor() {
    return Module['_gdspx_alloc_color']();
}

function FreeGdColor(ptr) {
    Module['_gdspx_free_color'](ptr);
}

function ToGdRect2(rect) {
    return Module['_gdspx_new_rect2'](rect['position']['x'], rect['position']['y'], rect['size']['x'], rect['size']['y']);
}

function ToJsRect2(ptr, out = { 'position': {}, 'size': {} }) {
    const HEAPF32 = Module['HEAPF32'];
    const floatIndex = ptr / 4;
    out['position']['x'] = HEAPF32[floatIndex];
    out['position']['y'] = HEAPF32[floatIndex + 1];
    out['size']['x'] = HEAPF32[floatIndex + 2];
    out['size']['y'] = HEAPF32[floatIndex + 3];
    return out;
}

function AllocGdRect2() {
    return Module['_gdspx_alloc_rect2']();
}

function FreeGdRect2(ptr) {
    Module['_gdspx_free_rect2'](ptr);
}

// -----------------------------------------------------------------------------
// 原生数组与 WASM 临时内存池
// -----------------------------------------------------------------------------

// 以下数组类型编号由 SPX 代码生成器维护，请勿只修改生成结果。
// BEGIN GENERATED ARRAY TYPES
const GDSPX_ARRAY_TAG = "__gdspx_array";
const GDSPX_ARRAY_TYPE_UNKNOWN = 0;
const GDSPX_ARRAY_TYPE_INT64 = 1;
const GDSPX_ARRAY_TYPE_FLOAT = 2;
const GDSPX_ARRAY_TYPE_BOOL = 3;
const GDSPX_ARRAY_TYPE_STRING = 4;
const GDSPX_ARRAY_TYPE_BYTE = 5;
const GDSPX_ARRAY_TYPE_GDOBJ = 6;

function NativeArrayElementSize(arrayType) {
    switch (arrayType) {
    case GDSPX_ARRAY_TYPE_INT64:
        return 8;
    case GDSPX_ARRAY_TYPE_FLOAT:
        return 4;
    case GDSPX_ARRAY_TYPE_BOOL:
        return 1;
    case GDSPX_ARRAY_TYPE_BYTE:
        return 1;
    case GDSPX_ARRAY_TYPE_GDOBJ:
        return 8;
    default:
        return 0;
    }
}
// END GENERATED ARRAY TYPES
const GDSPX_ARRAY_ARENA_BYTES = 1024 * 1024;
const GDSPX_ARRAY_ALIGNMENT = 8;
const GDSPX_ARRAY_POOL = "default";
const GDSPX_INPUT_POOL = "input";
const GDSPX_EMPTY_U8 = new Uint8Array(0);
const GDSPX_MAX_ARRAY_ELEMENTS = 16 * 1024 * 1024;
const GDSPX_MAX_ARRAY_BYTES = 256 * 1024 * 1024;

let arrayArenaModule = null;
const arrayArenas = new Map();
const deferredArenaFrees = [];
let arrayBorrowGeneration = 0;

// 只有这里创建的描述对象才允许把 WASM 指针交给原生调用。WeakMap 用来识别这些
// 可信对象；Object.freeze 后描述对象本身也可充当元数据，无需再复制一份。
const NativeArrays = (() => {
	// (() => { ... })() 是“立即执行函数”：定义后立即运行，用来形成私有作用域。
    const borrowed = new WeakMap();

    function borrow(type, count, byteLength, poolName = GDSPX_ARRAY_POOL) {
		// 从复用内存池中切出一段空间，并返回描述它的普通 JavaScript 对象。
        if (!HasActiveModuleHeap() || !IsNativeArrayByteLength(type, count, byteLength)) {
            return null;
        }
        const arena = GetArrayArena(byteLength, poolName);
        if (!arena) {
            return null;
        }
        const module = arena.module;
        const generation = arrayBorrowGeneration;
        const ptr = arena.ptr + arena.offset;
        arena.offset += AlignArrayBytes(byteLength);

        const array = Object.freeze({
            [GDSPX_ARRAY_TAG]: true,
            'type': type,
            'count': count,
            'ptr': ptr,
            'module': module,
            'byteLength': byteLength,
			// get data() 是读取器属性：读取 data 属性时才创建指向当前内存的视图。
            get 'data'() {
                return generation === arrayBorrowGeneration ? NativeArrayDataView(ptr, byteLength, module) : GDSPX_EMPTY_U8;
            },
        });
        borrowed.set(array, generation);
        return array;
    }

    function metadata(array) {
        return borrowed.has(array) ? array : null;
    }

    return Object.freeze({ borrow, metadata, isCurrent: array => borrowed.get(array) === arrayBorrowGeneration });
})();

const GdspxBorrowNativeArray = NativeArrays.borrow;

function HasActiveModule() {
	// typeof 用于安全检测尚未声明的全局变量；直接读取不存在的 Module 会抛异常。
    return typeof Module !== 'undefined' && Module !== null;
}

function HasActiveModuleHeap() {
    return HasActiveModule() && !!Module['HEAPU8'];
}

function FreeArrayArena(arena) {
    try {
        arena.free(arena.ptr);
    } catch {
		// 重启时旧 WASM 实例可能已经销毁，此时释放失败可以忽略。
    }
}

function AlignArrayBytes(size) {
    return Math.ceil(size / GDSPX_ARRAY_ALIGNMENT) * GDSPX_ARRAY_ALIGNMENT;
}

function ArrayArenaCapacity(minSize) {
    let capacity = GDSPX_ARRAY_ARENA_BYTES;
    while (capacity < minSize) {
        capacity *= 2;
    }
    return capacity;
}

function IsSafeArrayCount(value) {
    return Number.isSafeInteger(value) && value >= 0 && value <= GDSPX_MAX_ARRAY_ELEMENTS;
}

function IsHeapRange(ptr, byteLength) {
    if (!HasActiveModuleHeap() || !Number.isSafeInteger(ptr) || ptr < 0 ||
            !Number.isSafeInteger(byteLength) || byteLength < 0) {
        return false;
    }
    const heapLength = Module['HEAPU8'].length;
    return ptr <= heapLength && byteLength <= heapLength - ptr;
}

function NativeArrayDataView(ptr, byteLength, module) {
    if (!HasActiveModule() || module !== Module || !IsHeapRange(ptr, byteLength)) {
        return GDSPX_EMPTY_U8;
    }
    return module['HEAPU8'].subarray(ptr, ptr + byteLength);
}

function GdspxFlushDeferredFrees() {
	// 先让旧描述对象失效，再复用或释放其底层内存，避免旧对象访问到新数据。
    arrayBorrowGeneration += 1;
	// Update、reset、destroy 都会结束本轮临时数组借用，因此把所有内存池偏移归零。
    for (const arena of arrayArenas.values()) {
        arena.offset = 0;
    }
    for (const arena of deferredArenaFrees.splice(0)) {
        FreeArrayArena(arena);
    }
}

// 在当前内存块中预留空间；容量不足时换新块，但不立即破坏本轮更早创建的参数。
// 元素数量和字节数已由 borrow() 校验。
function GetArrayArena(byteLength, poolName) {
    const malloc = Module['_cmalloc'];
    const free = Module['_cfree'];
    if (typeof malloc !== 'function' || typeof free !== 'function') {
        return null;
    }
    if (arrayArenaModule !== Module) {
        for (const arena of arrayArenas.values()) {
            FreeArrayArena(arena);
        }
        arrayArenas.clear();
        arrayArenaModule = Module;
    }

    const pool = String(poolName || GDSPX_ARRAY_POOL);
    const previous = arrayArenas.get(pool);
    const required = AlignArrayBytes(byteLength);
    if (previous && required <= previous.capacity - previous.offset) {
        return previous;
    }

    const capacity = ArrayArenaCapacity(required);
    const ptr = malloc(capacity);
    if (!Number.isSafeInteger(ptr) || ptr <= 0 || ptr % GDSPX_ARRAY_ALIGNMENT !== 0 || !IsHeapRange(ptr, capacity)) {
        return null;
    }
    if (previous) {
        deferredArenaFrees.push(previous);
    }
    const arena = { ptr, capacity, offset: 0, module: Module, free };
    arrayArenas.set(pool, arena);
    return arena;
}

function IsNativeArrayByteLength(type, count, byteLength) {
    if (!IsSafeArrayCount(count) || !Number.isSafeInteger(byteLength) ||
            byteLength < 0 || byteLength > GDSPX_MAX_ARRAY_BYTES) {
        return false;
    }
    if (type === GDSPX_ARRAY_TYPE_STRING) {
        return count === 0 ? byteLength === 0 : byteLength >= count * 9;
    }
    const size = NativeArrayElementSize(type);
    return size > 0 && byteLength === count * size;
}

// 本文件借出的描述对象已经校验且不可变。外部传入的普通对象不能信任其中的 ptr
// 和 module 字段，因此这里只提取并重新校验类型、数量与实际 data。
function DescribeNativeArray(array) {
    if (!array || typeof array !== 'object') {
        return null;
    }
    const metadata = NativeArrays.metadata(array);
    if (metadata) {
        return NativeArrays.isCurrent(array) && HasActiveModule() && metadata['module'] === Module ? metadata : null;
    }
    const type = Number(array['type']);
    const count = Number(array['count']);
    const data = array['data'];
    const byteLength = Number(data && data.length);
    if (!IsNativeArrayByteLength(type, count, byteLength)) {
        return null;
    }
    return { 'type': type, 'count': count, 'byteLength': byteLength, 'data': data };
}

function NativeArrayCount(array) {
    const info = DescribeNativeArray(array);
    return info ? info['count'] : -1;
}

// 可写调用必须保留调用者提供的可信 WASM 存储。只读外部输入会被复制进 input
// 内存池，绝不直接采用外部对象自称的指针，避免越界或伪造地址。
function RequireNativeArrayBuffer(array, opName, expectedType = null, writable = false) {
    if (!array || array[GDSPX_ARRAY_TAG] !== true) {
        throw new Error(opName + " requires a native array");
    }
    const metadata = NativeArrays.metadata(array);
    if (writable && !metadata) {
        throw new Error(opName + " requires a pre-allocated Wasm array");
    }
    const info = DescribeNativeArray(array);
    if (!info) {
        throw new Error(opName + " requires a valid native array shape");
    }
    if (expectedType !== null && info['type'] !== expectedType) {
        throw new Error(opName + " received an incompatible native array type");
    }
    if (metadata) {
        if (!IsHeapRange(metadata['ptr'], metadata['byteLength'])) {
            throw new Error(opName + " requires accessible native array data");
        }
        return metadata;
    }

    const copy = GdspxBorrowNativeArray(info['type'], info['count'], info['byteLength'], GDSPX_INPUT_POOL);
    if (!copy) {
        throw new Error(opName + " failed to allocate native array input buffer");
    }
    copy['data'].set(info['data']);
    return copy;
}

function RequireNativeArray(array, opName, expectedType = null, writable = false) {
    return RequireNativeArrayBuffer(array, opName, expectedType, writable)['ptr'];
}

function ReadArrayOutput(exportName, type, count) {
	// 为固定长度返回值借一段可写内存，把地址传给 C++，再将同一个描述对象返回。
    if (!HasActiveModule()) {
        return null;
    }
    const call = Module[exportName];
    if (typeof call !== 'function') {
        return null;
    }
    const out = GdspxBorrowNativeArray(type, count, count * NativeArrayElementSize(type), exportName);
    if (!out) {
        return null;
    }
    call(out['ptr']);
    return out;
}

function ToGdArray(array) {
	// 创建一个很轻的 C++ 数组包装器，它借用现有数据，不再次复制数组内容。
    const input = RequireNativeArrayBuffer(array, "ToGdArray");
    const wrapper = Module['_gdspx_borrow_array'](input['ptr'], input['byteLength'], input['count'], input['type']);
    if (!wrapper) {
        throw new Error("Invalid native array data");
    }
    return wrapper;
}

function ToJsArray(wrapper) {
    const info = Module['_gdspx_get_array_info'](wrapper);
    if (!info) {
        return null;
    }
    if (!IsHeapRange(info, 12) || info % 4 !== 0) {
        return null;
    }
    const words = Module['HEAPU32'];
    const count = words[info / 4];
    const type = words[info / 4 + 1];
    const ptr = words[info / 4 + 2];
    if (!IsSafeArrayCount(count)) {
        return null;
    }
    let data;
    if (type === GDSPX_ARRAY_TYPE_STRING) {
        if (!IsHeapRange(ptr, count * 4) || ptr % 4 !== 0 || (count > 0 && ptr === 0)) {
            return null;
        }
        const heap = Module['HEAPU8'];
        const strings = [];
        let total = count * 8;
        for (let i = 0; i < count; i++) {
            const start = words[ptr / 4 + i];
            if (!start || !IsHeapRange(start, 1)) {
                return null;
            }
            let end = start;
            while (end < heap.length && heap[end] !== 0 && end - start < GDSPX_MAX_STRING_BYTES) {
                end++;
            }
            if (end === heap.length || heap[end] !== 0 || total > GDSPX_MAX_ARRAY_BYTES - (end - start + 1)) {
                return null;
            }
            strings.push([start, end]);
            total += end - start + 1;
        }
        data = new Uint8Array(total);
        const table = new DataView(data.buffer);
        let offset = count * 8;
        for (let i = 0; i < count; i++) {
            const [start, end] = strings[i];
            table.setUint32(i * 8, offset, true);
            table.setUint32(i * 8 + 4, end - start, true);
            data.set(heap.subarray(start, end), offset);
            offset += end - start + 1;
        }
    } else {
        const length = count * NativeArrayElementSize(type);
        if (!IsNativeArrayByteLength(type, count, length) || !IsHeapRange(ptr, length) ||
                (length > 0 && ptr === 0)) {
            return null;
        }
		// 生成的包装函数随后会释放原生结果，因此必须先 slice 复制并脱离 WASM 内存。
        data = Module['HEAPU8'].slice(ptr, ptr + length);
    }
    return { [GDSPX_ARRAY_TAG]: true, 'type': type, 'count': count, 'data': data };
}

function AllocGdArray() {
    return Module['_gdspx_alloc_array']();
}

function FreeGdArray(ptr) {
    Module['_gdspx_free_array'](ptr);
}

// 这两个函数会被 Go WASM 或单独编译的 Emscripten JS 库按名称调用。使用
// globalThis['固定名称'] 导出，可防止 Closure Compiler 高级压缩改掉 ABI 名称。
globalThis['GdspxFlushDeferredFrees'] = GdspxFlushDeferredFrees;
globalThis['GdspxBorrowNativeArray'] = GdspxBorrowNativeArray;
