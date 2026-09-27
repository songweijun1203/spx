// Go/JavaScript 与 Godot WASM 之间的线性内存适配层。
// 直接调用方：生成的 godot_js_spx.cpp 导出函数和 gdspx.util.js；
// 顶层调用方：Go manager_web.gen.go 发起的每次 Godot Manager 调用。
// JavaScript 只能持有 WASM 地址，不能拥有 C++ 对象；本文件用对象池分配稳定 wrapper，
// 用私有快照验证嵌套指针/数组元数据，并明确区分“拥有 payload”与“同步调用期借用”。

#include "../gdextension_spx_ext.h"
#include "core/extension/gdextension.h"

#include "godot_js_spx_util.h"
#include <cstdlib>
#include <cstdint>
#include <cstring>
#include <limits>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>
#include <emscripten/emscripten.h>

// 各 ABI 类型的 wrapper 池。池拥有 wrapper 本身；调用方从 alloc/new 借出，并必须
// 交给匹配的 free 函数归还。池和下方注册表都不是跨线程容器，桥调用需串行执行。
static ObjectPool<GdVec2> vec2Pool(100);
static ObjectPool<GdString> stringPool(100);
static ObjectPool<GdObj> objPool(100);
static ObjectPool<GdInt> intPool(100);
static ObjectPool<GdFloat> floatPool(100);
static ObjectPool<GdBool> boolPool(100);
static ObjectPool<GdVec3> vec3Pool(100);
static ObjectPool<GdVec4> vec4Pool(100);
static ObjectPool<GdColor> colorPool(100);
static ObjectPool<GdRect2> rect2Pool(100);
static ObjectPool<GdArray> arrayPool(100);

struct CachedGdStringEntry {
	// malloc 得到并以 NUL 结尾的 UTF-8 payload；缓存条目拥有并最终 free。
    char *data = nullptr;
	// payload 的字节长度，不包含末尾 NUL。
    uint32_t len = 0;
	// 当前有多少 GdString wrapper 借用该 payload；非零时禁止淘汰。
    uint64_t refcount = 0;
	// LRU 时间戳，用于在缓存满时淘汰最久未使用且 refcount=0 的条目。
    uint64_t last_used_tick = 0;
	// 不可变值副本，同时作为按内容查询的 map key 和篡改校验基准。
    std::string value;
};

// 仅缓存短字符串，限制条目数以避免跨 ABI 高频调用造成无界常驻内存。
static constexpr size_t GDSPX_STRING_CACHE_MAX_ENTRIES = 128;
static constexpr uint32_t GDSPX_STRING_CACHE_MAX_LEN = 256;
// 缓存拥有全部 CachedGdStringEntry；两个 map 只保存非拥有索引。
static std::vector<CachedGdStringEntry> gdspxStringCache;
static std::unordered_map<std::string, size_t> gdspxStringCacheByValue;
static std::unordered_map<const char *, size_t> gdspxStringCacheByPtr;
// 单调递增的进程内 LRU 时钟，仅用于相对新旧比较。
static uint64_t gdspxStringCacheTick = 0;

static CachedGdStringEntry *find_cached_gdstring_by_value(const char *str, uint32_t len);
static CachedGdStringEntry *find_cached_gdstring_by_ptr(const char *ptr);

namespace {

constexpr size_t GDSPX_MAX_STRING_BYTES = 256 * 1024 * 1024;
// 分配前限制元素数量，防止来自 JS/Go 的畸形长度导致整数溢出或过量分配。
constexpr int32_t GDSPX_MAX_ARRAY_ELEMENTS = 16 * 1024 * 1024;
// 原生传输缓冲上限，同时约束数值 payload 和字符串表/内容总量。
constexpr size_t GDSPX_MAX_ARRAY_BYTES = 256 * 1024 * 1024;

static bool checked_array_bytes(int32_t count, size_t element_size, size_t &r_bytes) {
    if (count < 0 || count > GDSPX_MAX_ARRAY_ELEMENTS || element_size == 0) {
        return false;
    }

    const size_t count_size = static_cast<size_t>(count);
    if (count_size > std::numeric_limits<size_t>::max() / element_size) {
        return false;
    }
    r_bytes = count_size * element_size;
    return true;
}

static bool array_element_size(int32_t type, size_t &r_element_size) {
    switch (type) {
        case GD_ARRAY_TYPE_INT64:
        case GD_ARRAY_TYPE_GDOBJ:
            r_element_size = sizeof(int64_t);
            return true;
        case GD_ARRAY_TYPE_FLOAT:
            r_element_size = sizeof(float);
            return true;
        case GD_ARRAY_TYPE_BOOL:
        case GD_ARRAY_TYPE_BYTE:
            r_element_size = sizeof(uint8_t);
            return true;
        default:
            return false;
    }
}

struct GdArrayStringSlotSnapshot {
	// 字符串 payload 的受信任地址；所有权由所属数组快照的 owns_strings 决定。
    char *ptr = nullptr;
	// UTF-8 字节数，不含末尾 NUL；释放前用于验证终止符仍在原边界。
    size_t len = 0;
};

// 数组元数据的受信任快照。JS 可以改 WASM 内存，因此释放时不能相信 live header。
struct GdArrayMetadataSnapshot {
	// malloc 的 GdArrayInfo header；快照注册表拥有并在 release 时释放。
    GdArrayInfo *info = nullptr;
	// 注册时验证过的元素数。
    int32_t size = 0;
	// GD_ARRAY_TYPE_* 元素类型。
    int32_t type = GD_ARRAY_TYPE_UNKNOWN;
	// 数值 payload 或字符串指针表地址。
    void *data = nullptr;
	// data 覆盖的受信任字节数。
    size_t data_bytes = 0;
	// 是否释放 data；借用数值数组为 false，拥有型数组/字符串指针表为 true。
    bool owns_data = true;
	// 是否逐项释放字符串 payload；借用字符串数组为 false。
    bool owns_strings = true;
	// 字符串数组各槽位的地址/长度快照，数值数组保持为空。
    std::vector<GdArrayStringSlotSnapshot> string_slots;
};

// 私有绑定阻止 JS 伪造 wrapper 元数据：wrapper->info、info->snapshot、info->owner
// 三者必须一致，且每个 payload 最多绑定一个活跃 wrapper。
static std::unordered_map<GdArray *, GdArrayInfo *> gdspxArrayBindings;
static std::unordered_map<GdArrayInfo *, GdArrayMetadataSnapshot> gdspxArraySnapshots;
static std::unordered_map<GdArrayInfo *, GdArray *> gdspxArrayOwners;

enum class GdStringReleaseKind {
	// 空 wrapper 或不拥有 payload。
    NONE,
	// Manager/构造函数返回的独占 malloc payload。
    MALLOC,
	// 字符串缓存拥有的共享 payload，释放 wrapper 时只递减引用计数。
    CACHE,
};

// 字符串 wrapper 的受信任所有权快照；不能用 JS 可改写的 *wrapper 决定 free 地址。
struct GdStringSnapshot {
	// UTF-8 payload 地址。
    const char *ptr = nullptr;
	// payload 字节长度，不含 NUL。
    uint32_t len = 0;
	// payload 的释放策略。
    GdStringReleaseKind release_kind = GdStringReleaseKind::NONE;
	// wrapper 是否已经与一次有效结果绑定；绑定后不允许替换嵌套指针。
    bool bound = false;
};

// 活跃 GdString wrapper 到受信任快照的映射；wrapper 本身仍由 stringPool 拥有。
static std::unordered_map<GdString *, GdStringSnapshot> gdspxStringSnapshots;

static size_t bounded_cstr_len(const char *str, size_t max_len) {
    if (str == nullptr) {
        return 0;
    }
    size_t len = 0;
    while (len < max_len && str[len] != '\0') {
        ++len;
    }
    return len;
}

static bool string_snapshot_matches_live(GdString *wrapper, const GdStringSnapshot &snapshot) {
	// live 值只用于比较，访问和释放始终使用不可由 JS 替换的快照地址。
    return wrapper != nullptr && *wrapper == snapshot.ptr;
}

static void release_string_snapshot(const GdStringSnapshot &snapshot) {
    if (snapshot.ptr == nullptr) {
        return;
    }

    if (snapshot.release_kind == GdStringReleaseKind::CACHE) {
		// 按快照地址查缓存；引用计数非零的条目不会被 LRU 淘汰。
        CachedGdStringEntry *cached = find_cached_gdstring_by_ptr(snapshot.ptr);
        if (cached != nullptr && cached->data == snapshot.ptr && cached->len == snapshot.len &&
                cached->refcount > 0) {
            cached->refcount -= 1;
        }
        return;
    }

    if (snapshot.release_kind == GdStringReleaseKind::MALLOC) {
        free(const_cast<char *>(snapshot.ptr));
    }
}

static bool make_manager_string_snapshot(GdString value, GdStringSnapshot &r_snapshot) {
    GdStringSnapshot snapshot;
    snapshot.bound = true;
    if (value == nullptr) {
        r_snapshot = snapshot;
        return true;
    }

    const char *ptr = static_cast<const char *>(value);
    const size_t len = bounded_cstr_len(ptr, GDSPX_MAX_STRING_BYTES + 1);
    if (len > GDSPX_MAX_STRING_BYTES || len > std::numeric_limits<uint32_t>::max()) {
        return false;
    }
    snapshot.ptr = ptr;
    snapshot.len = static_cast<uint32_t>(len);
    snapshot.release_kind = GdStringReleaseKind::MALLOC;
    r_snapshot = snapshot;
    return true;
}

static void discard_manager_string_result(GdString value, GdString *wrapper) {
    if (value == nullptr) {
        return;
    }
	// Manager 的结果 payload 由 malloc 分配；这里只丢弃 payload，wrapper 仍归对象池。
    if (value != static_cast<GdString>(wrapper)) {
        free(const_cast<void *>(value));
    }
}

static bool make_array_snapshot(GdArrayInfo *info, GdArrayMetadataSnapshot &r_snapshot) {
    if (info == nullptr) {
        return false;
    }

	// header 仅在注册时读取一次；后续只将 live header 与该快照比较。
    const int32_t size = info->size;
    const int32_t type = info->type;
    void *data = info->data;
    if (size < 0 || size > GDSPX_MAX_ARRAY_ELEMENTS) {
        return false;
    }

    GdArrayMetadataSnapshot snapshot;
    snapshot.info = info;
    snapshot.size = size;
    snapshot.type = type;
    snapshot.data = data;

	// header 与 payload 不得重叠，否则释放顺序会产生 double free/越界访问。
    if (data == info) {
        return false;
    }

    size_t element_size = 0;
    if (array_element_size(type, element_size)) {
        if (!checked_array_bytes(size, element_size, snapshot.data_bytes) ||
                snapshot.data_bytes > GDSPX_MAX_ARRAY_BYTES ||
                (size > 0 && data == nullptr) || (size == 0 && data != nullptr)) {
            return false;
        }
        r_snapshot = std::move(snapshot);
        return true;
    }

    if (type != GD_ARRAY_TYPE_STRING) {
        return false;
    }

    size_t slot_bytes = 0;
    if (!checked_array_bytes(size, sizeof(char *), slot_bytes) ||
            slot_bytes > GDSPX_MAX_ARRAY_BYTES || (size > 0 && data == nullptr) ||
            (size == 0 && data != nullptr)) {
        return false;
    }
    snapshot.data_bytes = slot_bytes;
    snapshot.string_slots.resize(static_cast<size_t>(size));
    size_t native_bytes = static_cast<size_t>(size) * 8;
    char **strings = static_cast<char **>(data);
    for (int32_t i = 0; i < size; ++i) {
        char *str = strings[i];
        if (str == reinterpret_cast<char *>(info) || str == reinterpret_cast<char *>(data)) {
            return false;
        }
        snapshot.string_slots[static_cast<size_t>(i)].ptr = str;
        if (str != nullptr) {
			// 扫描必须有上限；绑定后的验证还会检查原长度处终止符。
            const size_t len = bounded_cstr_len(str, GDSPX_MAX_ARRAY_BYTES + 1);
            if (len > GDSPX_MAX_ARRAY_BYTES) {
                return false;
            }
            snapshot.string_slots[static_cast<size_t>(i)].len = len;
        }

        const size_t length = snapshot.string_slots[static_cast<size_t>(i)].len;
        if (native_bytes > GDSPX_MAX_ARRAY_BYTES || length >= GDSPX_MAX_ARRAY_BYTES - native_bytes) {
            return false;
        }
        native_bytes += length + 1;
    }

    r_snapshot = std::move(snapshot);
    return true;
}

static bool array_snapshot_matches_live(const GdArrayMetadataSnapshot &snapshot) {
    GdArrayInfo *info = snapshot.info;
    if (info == nullptr || info->size != snapshot.size || info->type != snapshot.type ||
            info->data != snapshot.data) {
        return false;
    }

    if (snapshot.type != GD_ARRAY_TYPE_STRING) {
        return true;
    }

    if (snapshot.size == 0) {
        return snapshot.data == nullptr;
    }
    if (snapshot.data == nullptr || snapshot.string_slots.size() != static_cast<size_t>(snapshot.size)) {
        return false;
    }

    char **strings = static_cast<char **>(snapshot.data);
    for (int32_t i = 0; i < snapshot.size; ++i) {
        const GdArrayStringSlotSnapshot &expected = snapshot.string_slots[static_cast<size_t>(i)];
        char *current = strings[i];
        if (current != expected.ptr) {
            return false;
        }
        if (current != nullptr) {
            // Keep the original terminator within the recorded length.
            if (bounded_cstr_len(current, expected.len + 1) != expected.len) {
                return false;
            }
        }
    }
    return true;
}

static bool array_header_matches_snapshot(const GdArrayMetadataSnapshot &snapshot) {
    return snapshot.info != nullptr && snapshot.info->size == snapshot.size &&
            snapshot.info->type == snapshot.type && snapshot.info->data == snapshot.data;
}

static void free_array_snapshot(const GdArrayMetadataSnapshot &snapshot) {
    std::unordered_set<void *> freed_allocations;
    if (snapshot.owns_strings && snapshot.type == GD_ARRAY_TYPE_STRING) {
        for (const GdArrayStringSlotSnapshot &slot : snapshot.string_slots) {
            if (slot.ptr != nullptr && freed_allocations.insert(slot.ptr).second) {
                free(slot.ptr);
            }
        }
    }
    if (snapshot.owns_data && snapshot.data != nullptr && freed_allocations.insert(snapshot.data).second) {
        free(snapshot.data);
    }
    if (snapshot.info != nullptr && freed_allocations.insert(snapshot.info).second) {
        free(snapshot.info);
    }
}

static bool release_array_snapshot(GdArrayInfo *info, GdArray *expected_owner) {
    auto snapshot_it = gdspxArraySnapshots.find(info);
    if (snapshot_it == gdspxArraySnapshots.end()) {
        return false;
    }

    auto owner_it = gdspxArrayOwners.find(info);
    if (expected_owner == nullptr) {
		// Manager 侧只允许释放尚未绑定 wrapper 的构造中数组。
        if (owner_it != gdspxArrayOwners.end()) {
            return false;
        }

		// 字符串数组在绑定前允许 Manager 填槽，释放前重新采集最终受信任槽位。
        if (array_header_matches_snapshot(snapshot_it->second)) {
            GdArrayMetadataSnapshot current_snapshot;
            if (make_array_snapshot(info, current_snapshot)) {
                current_snapshot.owns_data = snapshot_it->second.owns_data;
                current_snapshot.owns_strings = snapshot_it->second.owns_strings;
                snapshot_it->second = std::move(current_snapshot);
            }
        }
    } else {
        if (owner_it == gdspxArrayOwners.end() || owner_it->second != expected_owner) {
            return false;
        }
        gdspxArrayBindings.erase(expected_owner);
        gdspxArrayOwners.erase(owner_it);
        if (arrayPool.is_active(expected_owner)) {
            *expected_owner = nullptr;
        }
    }

    GdArrayMetadataSnapshot snapshot = std::move(snapshot_it->second);
    gdspxArraySnapshots.erase(snapshot_it);
    free_array_snapshot(snapshot);
    return true;
}

} // namespace

extern "C" bool gdspx_bind_array_wrapper(GdArray *wrapper) {
	// 直接调用方：生成的 C++ 导出层在 Manager 返回数组后绑定结果 wrapper；
	// 顶层调用方：gdspx.js/Go 对任一返回 GdArray 的 Manager API 调用。
    if (wrapper == nullptr || !arrayPool.is_active(wrapper)) {
        return false;
    }

    GdArrayInfo *info = *wrapper;
    auto binding_it = gdspxArrayBindings.find(wrapper);
    if (info == nullptr) {
		// null 只对从未绑定过的空 wrapper 有效。
        return binding_it == gdspxArrayBindings.end();
    }

    if (binding_it != gdspxArrayBindings.end()) {
		// 绑定不可变；重复调用仅在 wrapper 和 payload 都未被篡改时成功。
        if (binding_it->second != info) {
            return false;
        }
        auto snapshot_it = gdspxArraySnapshots.find(info);
        return snapshot_it != gdspxArraySnapshots.end() &&
                array_snapshot_matches_live(snapshot_it->second);
    }

    auto snapshot_it = gdspxArraySnapshots.find(info);
    if (snapshot_it == gdspxArraySnapshots.end() ||
            !array_header_matches_snapshot(snapshot_it->second)) {
		// 只有 C++ 内部注册过的 info 才能成为受信任 payload。
        return false;
    }

    auto owner_it = gdspxArrayOwners.find(info);
    if (owner_it != gdspxArrayOwners.end() && owner_it->second != wrapper) {
		// 一个 payload 同时只能有一个 wrapper owner，防止重复释放。
        return false;
    }

	// 数组跨入 wrapper 时封存字符串槽位，之后 live 槽位不允许变化。
    GdArrayMetadataSnapshot sealed_snapshot;
    if (!make_array_snapshot(info, sealed_snapshot)) {
        return false;
    }
    sealed_snapshot.owns_data = snapshot_it->second.owns_data;
    sealed_snapshot.owns_strings = snapshot_it->second.owns_strings;
    snapshot_it->second = std::move(sealed_snapshot);

    gdspxArrayOwners[info] = wrapper;
    gdspxArrayBindings[wrapper] = info;
    return true;
}

extern "C" bool gdspx_validate_array_wrapper(GdArray *wrapper) {
    if (wrapper == nullptr || !arrayPool.is_active(wrapper)) {
        return false;
    }

    auto binding_it = gdspxArrayBindings.find(wrapper);
    if (*wrapper == nullptr) {
		// 保留可空数组语义，但拒绝被改成 null 的已绑定 wrapper。
        return binding_it == gdspxArrayBindings.end();
    }
    if (binding_it == gdspxArrayBindings.end() || binding_it->second != *wrapper) {
        return false;
    }

    auto owner_it = gdspxArrayOwners.find(binding_it->second);
    auto snapshot_it = gdspxArraySnapshots.find(binding_it->second);
    return owner_it != gdspxArrayOwners.end() && owner_it->second == wrapper &&
            snapshot_it != gdspxArraySnapshots.end() &&
            array_snapshot_matches_live(snapshot_it->second);
}

extern "C" bool gdspx_prepare_array_wrapper(GdArray *wrapper) {
    return wrapper != nullptr && arrayPool.is_active(wrapper) && *wrapper == nullptr &&
            gdspxArrayBindings.find(wrapper) == gdspxArrayBindings.end();
}

extern "C" bool gdspx_validate_array_info(GdArray array) {
    if (array == nullptr) {
        return false;
    }
    auto snapshot_it = gdspxArraySnapshots.find(array);
    if (snapshot_it == gdspxArraySnapshots.end()) {
        return false;
    }
	// Manager 可在绑定前填充字符串槽；一旦绑定，数组内容结构即封存。
    if (gdspxArrayOwners.find(array) == gdspxArrayOwners.end()) {
        return array_header_matches_snapshot(snapshot_it->second);
    }
    return array_snapshot_matches_live(snapshot_it->second);
}

extern "C" bool gdspx_register_array_info(GdArray array) {
    if (array == nullptr) {
        return false;
    }

    auto snapshot_it = gdspxArraySnapshots.find(array);
    if (snapshot_it != gdspxArraySnapshots.end()) {
        return gdspxArrayOwners.find(array) == gdspxArrayOwners.end() &&
                array_header_matches_snapshot(snapshot_it->second);
    }

    GdArrayMetadataSnapshot snapshot;
    if (!make_array_snapshot(array, snapshot)) {
        return false;
    }
    gdspxArraySnapshots.emplace(array, std::move(snapshot));
    return true;
}

extern "C" bool gdspx_release_array_info(GdArray array) {
    return release_array_snapshot(array, nullptr);
}

static CachedGdStringEntry *find_cached_gdstring_by_value(const char *str, uint32_t len) {
    auto it = gdspxStringCacheByValue.find(std::string(str, len));
    if (it == gdspxStringCacheByValue.end()) {
        return nullptr;
    }
    return &gdspxStringCache[it->second];
}

static CachedGdStringEntry *find_cached_gdstring_by_ptr(const char *ptr) {
    auto it = gdspxStringCacheByPtr.find(ptr);
    if (it == gdspxStringCacheByPtr.end()) {
        return nullptr;
    }
    return &gdspxStringCache[it->second];
}

static bool should_cache_gdstring(uint32_t len) {
    return len <= GDSPX_STRING_CACHE_MAX_LEN;
}

static void remove_cached_gdstring_at(size_t index) {
    CachedGdStringEntry &entry = gdspxStringCache[index];
    gdspxStringCacheByValue.erase(entry.value);
    gdspxStringCacheByPtr.erase(entry.data);
    free(entry.data);

    size_t last_index = gdspxStringCache.size() - 1;
    if (index != last_index) {
        std::swap(gdspxStringCache[index], gdspxStringCache[last_index]);
        const CachedGdStringEntry &moved = gdspxStringCache[index];
        gdspxStringCacheByValue[moved.value] = index;
        gdspxStringCacheByPtr[moved.data] = index;
    }
    gdspxStringCache.pop_back();
}

static bool evict_oldest_unused_gdstring() {
    size_t oldest_index = static_cast<size_t>(-1);
    uint64_t oldest_tick = UINT64_MAX;
    for (size_t i = 0; i < gdspxStringCache.size(); i++) {
        const auto &entry = gdspxStringCache[i];
        if (entry.refcount == 0 && entry.last_used_tick < oldest_tick) {
            oldest_tick = entry.last_used_tick;
            oldest_index = i;
        }
    }
    if (oldest_index == static_cast<size_t>(-1)) {
        return false;
    }
    remove_cached_gdstring_at(oldest_index);
    return true;
}

static uint32_t readUint32LE(const uint8_t *bytes) {
    return uint32_t(bytes[0]) | (uint32_t(bytes[1]) << 8) |
            (uint32_t(bytes[2]) << 16) | (uint32_t(bytes[3]) << 24);
}

static_assert(sizeof(bool) == 1, "Boolean size must be 1 byte for web array bridge");
static_assert(sizeof(GdInt) == sizeof(uint64_t), "GdInt must be 64-bit for web ABI");
static_assert(sizeof(GdObj) == sizeof(uint64_t), "GdObj must be 64-bit for web ABI");
static_assert(sizeof(GdFloat) == sizeof(float), "Web GdFloat ABI requires single precision");

extern "C" {

// 以下 EMSCRIPTEN_KEEPALIVE 函数由 gdspx.util.js 通过 Module['_name'] 直接调用；
// 顶层来源为 Go Web Manager。extern "C" 固定导出名，KEEPALIVE 防止链接裁剪。
// cmalloc/cfree 管理 JS 写入 WASM 线性内存时使用的原始临时缓冲。
EMSCRIPTEN_KEEPALIVE
void *cmalloc(int size) {
    return malloc(size);
}

EMSCRIPTEN_KEEPALIVE
void cfree(void *ptr) {
    free(ptr);
}

// 兼容旧包装代码的浮点数组读取入口。
EMSCRIPTEN_KEEPALIVE
float gdspx_get_value(float* array, int idx) {
    return array[idx];
}


// 标量和 POD 结构 wrapper 只归对象池所有；alloc/new 与同类型 free 必须成对。
EMSCRIPTEN_KEEPALIVE
GdBool* gdspx_alloc_bool() {
    return boolPool.acquire();
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_bool(GdBool* b) {
	if (b == nullptr || !boolPool.is_active(b)) {
		return;
	}
    boolPool.release(b);
}


// float functions
EMSCRIPTEN_KEEPALIVE
GdFloat* gdspx_alloc_float() {
    return floatPool.acquire();
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_float(GdFloat* f) {
	if (f == nullptr || !floatPool.is_active(f)) {
		return;
	}
    floatPool.release(f);
}

// int functions
EMSCRIPTEN_KEEPALIVE
GdInt* gdspx_alloc_int() {
    return intPool.acquire();
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_int(GdInt* i) {
    if (i == nullptr || !intPool.is_active(i)) {
        return;
    }
    *i = 0;
    intPool.release(i);
}

// object functions
EMSCRIPTEN_KEEPALIVE
GdObj* gdspx_alloc_obj() {
    return objPool.acquire();
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_obj(GdObj* obj) {
    if (obj == nullptr || !objPool.is_active(obj)) {
        return;
    }
    *obj = 0;
    objPool.release(obj);
}

// vec2 functions
EMSCRIPTEN_KEEPALIVE
GdVec2* gdspx_alloc_vec2() {
    return vec2Pool.acquire();
}

EMSCRIPTEN_KEEPALIVE
GdVec2* gdspx_new_vec2(float x, float y) {
    GdVec2* ptr = gdspx_alloc_vec2();
    if (ptr == nullptr) {
        return nullptr;
    }
    ptr->x = x;
    ptr->y = y;
    return ptr;
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_vec2(GdVec2* vec) {
	if (vec == nullptr || !vec2Pool.is_active(vec)) {
		return;
	}
    vec2Pool.release(vec);
}

// vec3 functions
EMSCRIPTEN_KEEPALIVE
GdVec3* gdspx_alloc_vec3() {
    return vec3Pool.acquire();
}

EMSCRIPTEN_KEEPALIVE
GdVec3* gdspx_new_vec3(float x, float y, float z) {
    GdVec3* ptr= gdspx_alloc_vec3();
    if (ptr == nullptr) {
        return nullptr;
    }
    ptr->x = x;
    ptr->y = y;
    ptr->z = z;
    return ptr;
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_vec3(GdVec3* vec) {
	if (vec == nullptr || !vec3Pool.is_active(vec)) {
		return;
	}
    vec3Pool.release(vec);
}

// vec4 functions
EMSCRIPTEN_KEEPALIVE
GdVec4* gdspx_alloc_vec4() {
    return vec4Pool.acquire();
}

EMSCRIPTEN_KEEPALIVE
GdVec4* gdspx_new_vec4(float x, float y, float z, float w) {
    GdVec4* ptr = gdspx_alloc_vec4();
    if (ptr == nullptr) {
        return nullptr;
    }
    ptr->x = x;
    ptr->y = y;
    ptr->z = z;
    ptr->w = w;
    return ptr;
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_vec4(GdVec4* vec) {
	if (vec == nullptr || !vec4Pool.is_active(vec)) {
		return;
	}
    vec4Pool.release(vec);
}

// color functions
EMSCRIPTEN_KEEPALIVE
GdColor* gdspx_alloc_color() {
    return colorPool.acquire();
}

EMSCRIPTEN_KEEPALIVE
GdColor* gdspx_new_color(float r, float g, float b, float a) {
    GdColor* ptr = gdspx_alloc_color();
    if (ptr == nullptr) {
        return nullptr;
    }
    ptr->r = r;
    ptr->g = g;
    ptr->b = b;
    ptr->a = a;
    return ptr;
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_color(GdColor* color) {
	if (color == nullptr || !colorPool.is_active(color)) {
		return;
	}
    colorPool.release(color);
}

// rect2 functions
EMSCRIPTEN_KEEPALIVE
GdRect2* gdspx_alloc_rect2() {
    return rect2Pool.acquire();
}

EMSCRIPTEN_KEEPALIVE
GdRect2* gdspx_new_rect2(float x, float y, float width, float height) {
    GdRect2* ptr = gdspx_alloc_rect2();
    if (ptr == nullptr) {
        return nullptr;
    }
    ptr->position.x = x;
    ptr->position.y = y;
    ptr->size.width = width;
    ptr->size.height = height;
    return ptr;
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_rect2(GdRect2* rect) {
	if (rect == nullptr || !rect2Pool.is_active(rect)) {
		return;
	}
    rect2Pool.release(rect);
}

// string functions
EMSCRIPTEN_KEEPALIVE
GdString* gdspx_alloc_string() {
    GdString *wrapper = stringPool.acquire();
    if (wrapper == nullptr) {
        return nullptr;
    }

	// wrapper 复用前清理遗留嵌套指针和所有权，防止旧 payload 泄漏或重复释放。
    auto stale_it = gdspxStringSnapshots.find(wrapper);
    if (stale_it != gdspxStringSnapshots.end()) {
        GdStringSnapshot stale = stale_it->second;
        gdspxStringSnapshots.erase(stale_it);
        release_string_snapshot(stale);
    }
    *wrapper = nullptr;
    gdspxStringSnapshots.emplace(wrapper, GdStringSnapshot{});
    return wrapper;
}

EMSCRIPTEN_KEEPALIVE
GdString* gdspx_new_string(const char* str, uint32_t len) {
    if ((str == nullptr && len != 0) || static_cast<size_t>(len) > GDSPX_MAX_STRING_BYTES) {
        return nullptr;
    }
    const size_t allocation_size = static_cast<size_t>(len) + 1;
    if (allocation_size <= static_cast<size_t>(len)) {
        return nullptr;
    }

    const char *input = str != nullptr ? str : "";
    GdString* ptr = gdspx_alloc_string();
    if (ptr == nullptr) {
        return nullptr;
    }
    CachedGdStringEntry *cached = should_cache_gdstring(len) ?
            find_cached_gdstring_by_value(input, len) : nullptr;
    const bool cache_key_occupied = cached != nullptr;
    const bool cached_value_intact = cached != nullptr && cached->data != nullptr &&
            cached->len == len && cached->value.size() == static_cast<size_t>(len) &&
            memcmp(cached->data, cached->value.data(), len) == 0 && cached->data[len] == '\0';
    if (cached_value_intact && cached->refcount != std::numeric_limits<uint64_t>::max()) {
        cached->refcount += 1;
        cached->last_used_tick = ++gdspxStringCacheTick;
        gdspxStringSnapshots[ptr] = GdStringSnapshot{
            cached->data,
            cached->len,
            GdStringReleaseKind::CACHE,
            true,
        };
        *ptr = cached->data;
        return ptr;
    }

    char* result = (char*)malloc(allocation_size);
    if (result == nullptr) {
        gdspxStringSnapshots.erase(ptr);
        stringPool.release(ptr);
        return nullptr;
    }
    if (len > 0) {
        memcpy(result, input, len);
    }
    result[len] = '\0';

    GdStringReleaseKind release_kind = GdStringReleaseKind::MALLOC;
    if (should_cache_gdstring(len) && !cache_key_occupied) {
        bool cache_has_room = gdspxStringCache.size() < GDSPX_STRING_CACHE_MAX_ENTRIES;
        if (!cache_has_room) {
            cache_has_room = evict_oldest_unused_gdstring();
        }
        if (cache_has_room) {
            size_t cache_index = gdspxStringCache.size();
            gdspxStringCache.push_back(CachedGdStringEntry{
                result,
                len,
                1,
                ++gdspxStringCacheTick,
                std::string(result, len),
            });
            gdspxStringCacheByValue[gdspxStringCache.back().value] = cache_index;
            gdspxStringCacheByPtr[result] = cache_index;
            release_kind = GdStringReleaseKind::CACHE;
        }
    }

    gdspxStringSnapshots[ptr] = GdStringSnapshot{
        result,
        len,
        release_kind,
        true,
    };
    *ptr = result;
    return ptr;
}

bool gdspx_prepare_string_wrapper(GdString *wrapper) {
    if (wrapper == nullptr || !stringPool.is_active(wrapper)) {
        return false;
    }
    auto snapshot_it = gdspxStringSnapshots.find(wrapper);
    return snapshot_it != gdspxStringSnapshots.end() && !snapshot_it->second.bound &&
            snapshot_it->second.ptr == nullptr &&
            snapshot_it->second.release_kind == GdStringReleaseKind::NONE &&
            string_snapshot_matches_live(wrapper, snapshot_it->second);
}

bool gdspx_bind_string_wrapper(GdString *wrapper, GdString value) {
	// 直接调用方：生成的 C++ 导出层处理 Manager 的字符串返回值；
	// value 的 malloc payload 所有权在成功后转给 wrapper 快照。
    if (!gdspx_prepare_string_wrapper(wrapper) || value == static_cast<GdString>(wrapper)) {
        discard_manager_string_result(value, wrapper);
        return false;
    }

    GdStringSnapshot snapshot;
    if (!make_manager_string_snapshot(value, snapshot)) {
        discard_manager_string_result(value, wrapper);
        return false;
    }

	// 先发布受信任元数据，再向 JS 可见内存写嵌套指针，避免短暂的不一致状态。
    gdspxStringSnapshots[wrapper] = snapshot;
    *wrapper = value;
    return true;
}

bool gdspx_validate_string_wrapper(GdString *wrapper) {
    if (wrapper == nullptr || !stringPool.is_active(wrapper)) {
        return false;
    }
    auto snapshot_it = gdspxStringSnapshots.find(wrapper);
    return snapshot_it != gdspxStringSnapshots.end() && snapshot_it->second.bound &&
            string_snapshot_matches_live(wrapper, snapshot_it->second);
}

bool gdspx_get_string_value(GdString *wrapper, GdString *r_value) {
    if (r_value == nullptr) {
        return false;
    }
    *r_value = nullptr;
    if (!gdspx_validate_string_wrapper(wrapper)) {
        return false;
    }
    const GdStringSnapshot &snapshot = gdspxStringSnapshots.find(wrapper)->second;
	// Manager 把 GdString 当 C 字符串消费；只检查受信任长度边界处的终止符，
	// 不沿 JS 可改写的 wrapper 值重新扫描。
    if (snapshot.ptr != nullptr && snapshot.ptr[snapshot.len] != '\0') {
        return false;
    }
    *r_value = static_cast<GdString>(snapshot.ptr);
    return true;
}

EMSCRIPTEN_KEEPALIVE
const char* gdspx_get_string(GdString* ptr) {
    GdString value = nullptr;
    if (!gdspx_get_string_value(ptr, &value)) {
        return nullptr;
    }
    return static_cast<const char *>(value);
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_cstr(const char* str) {
	// 兼容旧 API：payload 所有权始终跟随 wrapper，因此这里刻意不释放。
    (void)str;
}

EMSCRIPTEN_KEEPALIVE
int32_t gdspx_get_string_len(GdString* ptr) {
    if (!gdspx_validate_string_wrapper(ptr)) {
        return 0;
    }
    const uint32_t len = gdspxStringSnapshots.find(ptr)->second.len;
    if (len > static_cast<uint32_t>(std::numeric_limits<int32_t>::max())) {
        return 0;
    }
    return static_cast<int32_t>(len);
}

EMSCRIPTEN_KEEPALIVE
void gdspx_free_string(GdString* p_gdstr) {
    if (p_gdstr == nullptr || !stringPool.is_active(p_gdstr)) {
        return;
    }

    auto snapshot_it = gdspxStringSnapshots.find(p_gdstr);
    if (snapshot_it != gdspxStringSnapshots.end()) {
		// 可检测 live 指针篡改，但释放动作始终依据快照，绝不 free 伪造地址。
        const bool live_pointer_matches =
                string_snapshot_matches_live(p_gdstr, snapshot_it->second);
        (void)live_pointer_matches;
        GdStringSnapshot snapshot = snapshot_it->second;
        gdspxStringSnapshots.erase(snapshot_it);
        release_string_snapshot(snapshot);
    }
    *p_gdstr = nullptr;
    stringPool.release(p_gdstr);
}



// 数组 wrapper 与 GdArrayInfo/payload 是两级所有权，必须通过私有绑定表释放。
EMSCRIPTEN_KEEPALIVE
GdArray* gdspx_alloc_array() {
    GdArray *wrapper = arrayPool.acquire();
    if (wrapper != nullptr) {
		// wrapper 可能被跨调用复用，旧嵌套指针和 C++ 绑定绝不能跨越对象池复用。
        auto binding_it = gdspxArrayBindings.find(wrapper);
        if (binding_it != gdspxArrayBindings.end()) {
            GdArrayInfo *stale_info = binding_it->second;
            if (!release_array_snapshot(stale_info, wrapper)) {
                gdspxArrayBindings.erase(wrapper);
                auto owner_it = gdspxArrayOwners.find(stale_info);
                if (owner_it != gdspxArrayOwners.end() && owner_it->second == wrapper &&
                        gdspxArraySnapshots.find(stale_info) == gdspxArraySnapshots.end()) {
                    gdspxArrayOwners.erase(owner_it);
                }
            }
        }
        *wrapper = nullptr;
    }
    return wrapper;
}


EMSCRIPTEN_KEEPALIVE
void gdspx_free_array(GdArray* p_gdstr) {
    if (p_gdstr == nullptr || !arrayPool.is_active(p_gdstr)) {
        return;
    }

    auto binding_it = gdspxArrayBindings.find(p_gdstr);
    if (binding_it == gdspxArrayBindings.end()) {
		// 未绑定 wrapper 的嵌套指针可能由 JS 伪造，只清空而不释放。
        *p_gdstr = nullptr;
        arrayPool.release(p_gdstr);
        return;
    }

    GdArrayInfo *info = binding_it->second;
    if (!release_array_snapshot(info, p_gdstr)) {
		// 受信任元数据缺失时闭锁失败：解除 wrapper，不尝试释放未知地址。
        gdspxArrayBindings.erase(binding_it);
        *p_gdstr = nullptr;
    }
    arrayPool.release(p_gdstr);
}

// 输入数组仅在同步 Manager 调用期间借用 bytes；wrapper 拥有描述符，字符串数组时
// 还拥有原生指针表，但永不拥有 bytes 内的元素 payload。
EMSCRIPTEN_KEEPALIVE
GdArray *gdspx_borrow_array(uint8_t *bytes, int byte_size, int32_t count, int32_t type) {
    if (count < 0 || count > GDSPX_MAX_ARRAY_ELEMENTS || byte_size < 0 ||
            static_cast<size_t>(byte_size) > GDSPX_MAX_ARRAY_BYTES ||
            (byte_size > 0 && bytes == nullptr)) {
        return nullptr;
    }
    size_t element_size = 0;
    const bool strings = type == GD_ARRAY_TYPE_STRING;
    if (strings) {
        const size_t table_size = static_cast<size_t>(count) * 8;
        if (table_size > static_cast<size_t>(byte_size)) {
            return nullptr;
        }
        size_t offset = table_size;
        for (int32_t i = 0; i < count; ++i) {
            const uint32_t start = readUint32LE(bytes + static_cast<size_t>(i) * 8);
            const uint32_t length = readUint32LE(bytes + static_cast<size_t>(i) * 8 + 4);
            if (start != offset || length >= static_cast<size_t>(byte_size) - offset ||
                    bytes[offset + length] != 0) {
                return nullptr;
            }
            offset += static_cast<size_t>(length) + 1;
        }
        if (offset != static_cast<size_t>(byte_size)) {
            return nullptr;
        }
    } else {
        size_t expected = 0;
        if (!array_element_size(type, element_size) ||
                !checked_array_bytes(count, element_size, expected) ||
                expected != static_cast<size_t>(byte_size) ||
                (count > 0 && reinterpret_cast<uintptr_t>(bytes) % element_size != 0)) {
            return nullptr;
        }
        if (type == GD_ARRAY_TYPE_BOOL) {
            for (int32_t i = 0; i < count; ++i) {
                if (bytes[i] > 1) {
                    return nullptr;
                }
            }
        }
    }
    GdArrayInfo *info = static_cast<GdArrayInfo *>(malloc(sizeof(GdArrayInfo)));
    if (info == nullptr) {
        return nullptr;
    }
    info->size = count;
    info->type = type;
    info->data = count > 0 ? bytes : nullptr;
    if (strings && count > 0) {
        char **slots = static_cast<char **>(malloc(static_cast<size_t>(count) * sizeof(char *)));
        if (slots == nullptr) {
            free(info);
            return nullptr;
        }
        for (int32_t i = 0; i < count; ++i) {
            slots[i] = reinterpret_cast<char *>(bytes + readUint32LE(bytes + static_cast<size_t>(i) * 8));
        }
        info->data = slots;
    }
    GdArrayMetadataSnapshot snapshot;
    if (!make_array_snapshot(info, snapshot)) {
        if (strings) {
            free(info->data);
        }
        free(info);
        return nullptr;
    }
    snapshot.owns_data = strings;
    snapshot.owns_strings = false;
    gdspxArraySnapshots.emplace(info, std::move(snapshot));

    GdArray *wrapper = gdspx_alloc_array();
    if (wrapper == nullptr) {
        gdspx_release_array_info(info);
        return nullptr;
    }
    *wrapper = info;
    if (!gdspx_bind_array_wrapper(wrapper)) {
        *wrapper = nullptr;
        gdspx_release_array_info(info);
        arrayPool.release(wrapper);
        return nullptr;
    }
    return wrapper;
}

// 返回的只读视图在 wrapper 释放前有效；调用方不得跨异步边界保存。
EMSCRIPTEN_KEEPALIVE
const GdArrayInfo *gdspx_get_array_info(GdArray *wrapper) {
    return gdspx_validate_array_wrapper(wrapper) ? *wrapper : nullptr;
}

}// extern "C"
