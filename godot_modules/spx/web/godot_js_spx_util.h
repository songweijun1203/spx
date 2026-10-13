#ifndef GODOT_JS_SPX_UTIL_H
#define GODOT_JS_SPX_UTIL_H

#include "../gdextension_spx_ext.h"
#include <algorithm>
#include <new>
#include <vector>

// Web ABI 的 GdString/GdArray wrapper 是对象池分配的二级指针。
// 直接调用方：gdspx.js 的参数编解码和生成的 godot_js_spx.cpp 导出函数；
// 顶层调用方：Go WASM 对 SPX/Godot API 的调用。
// Emscripten 规则：JS 与 C++ 只共享 WASM 线性内存；wrapper 与真实值的绑定保存在
// C++ 私有注册表，JS 不得直接写内存伪造绑定，且借用数组必须在释放 wrapper 前保持有效。
#ifdef __cplusplus
extern "C" {
#endif
bool gdspx_bind_string_wrapper(GdString *wrapper, GdString value);
bool gdspx_get_string_value(GdString *wrapper, GdString *r_value);
bool gdspx_prepare_string_wrapper(GdString *wrapper);
bool gdspx_validate_string_wrapper(GdString *wrapper);
bool gdspx_bind_array_wrapper(GdArray *wrapper);
bool gdspx_validate_array_wrapper(GdArray *wrapper);
bool gdspx_prepare_array_wrapper(GdArray *wrapper);
bool gdspx_validate_array_info(GdArray array);
bool gdspx_register_array_info(GdArray array);
bool gdspx_release_array_info(GdArray array);
// 输入数组在 wrapper 释放前借用原生数据；字符串数组的表项为 [uint32 offset,
// uint32 length]，后接以 NUL 终止的 UTF-8 字节。
GdArray *gdspx_borrow_array(uint8_t *data, int byte_size, int32_t count, int32_t type);
const GdArrayInfo *gdspx_get_array_info(GdArray *wrapper);
#ifdef __cplusplus
}
#endif

template <typename T>
// Web ABI 临时返回值对象池，减少 Go/JS 高频跨语言调用中的 malloc/delete。
// 直接调用方：godot_js_spx_util.cpp 的 gdspx_alloc_*/gdspx_free_* 导出函数。
// 本类拥有 allocated 中全部对象；pool 只保存当前空闲对象，active 对象仍归池所有。
class ObjectPool {
public:
    explicit ObjectPool(size_t size) {
        for (size_t i = 0; i < size; ++i) {
            T *obj = new (std::nothrow) T();
            if (obj != nullptr) {
                pool.push_back(obj);
                allocated.push_back(obj);
            }
        }
    }

    ~ObjectPool() {
        for (auto obj : allocated) {
            delete obj;
        }
    }

    T* acquire() {
        if (pool.empty()) {
            T *obj = new (std::nothrow) T();
            if (obj != nullptr) {
                allocated.push_back(obj);
            }
            return obj;
        } else {
            T* obj = pool.back();
            pool.pop_back();
            return obj;
        }
    }

    bool is_active(T *obj) const {
        if (obj == nullptr || std::find(allocated.begin(), allocated.end(), obj) == allocated.end()) {
            return false;
        }
        return std::find(pool.begin(), pool.end(), obj) == pool.end();
    }

    void release(T* obj) {
        if(obj == nullptr) {
            print_error("ObjectPool::release called with null pointer");
            return;
        }
        if (std::find(pool.begin(), pool.end(), obj) != pool.end()) {
            print_error("ObjectPool::release called twice for the same pointer");
            return;
        }
        if (std::find(allocated.begin(), allocated.end(), obj) == allocated.end()) {
            print_error("ObjectPool::release called for an unowned pointer");
            return;
        }
        pool.push_back(obj);
    }

private:
    std::vector<T*> pool; // 当前可复用的空闲对象。
    std::vector<T*> allocated; // 池创建过的全部对象，析构时统一 delete。
};

#endif // GODOT_JS_SPX_UTIL_H
