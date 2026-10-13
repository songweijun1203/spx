/**************************************************************************/
/*  spx_object_mgr.h                                                        */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/
/* Copyright (c) 2014-present Godot Engine contributors (see AUTHORS.md). */
/* Copyright (c) 2007-2014 Juan Linietsky, Ariel Manzur.                  */
/*                                                                        */
/* Permission is hereby granted, free of charge, to any person obtaining  */
/* a copy of this software and associated documentation files (the        */
/* "Software"), to deal in the Software without restriction, including    */
/* without limitation the rights to use, copy, modify, merge, publish,    */
/* distribute, sublicense, and/or sell copies of the Software, and to     */
/* permit persons to whom the Software is furnished to do so, subject to  */
/* the following conditions:                                              */
/*                                                                        */
/* The above copyright notice and this permission notice shall be         */
/* included in all copies or substantial portions of the Software.        */
/*                                                                        */
/* THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,        */
/* EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF     */
/* MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. */
/* IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY   */
/* CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,   */
/* TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE      */
/* SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.                 */
/**************************************************************************/

#ifndef SPX_OBJECT_MGR_H
#define SPX_OBJECT_MGR_H

#include "core/os/thread.h"
#include "core/templates/hash_map.h"
#include "scene/2d/node_2d.h"
#include "spx_manager.h"

// 按 GdObj ID 管理一类 SPX C++ 包装对象的通用 Manager。
// 本类在 Godot 主线程独占 T 对象及其共享 Node2D 根节点；返回指针仅借用于当前主线程任务，
// 不得跨帧、跨线程或跨 reset 保存。工作线程必须先由上层排队，再进入本 Manager。
// 直接调用方：Sprite/UI 等具体 Manager；顶层调用方：Go 对象创建、更新和销毁 API。
template <typename T>
class SpxObjectMgr : public SpxManager {
protected:
	// ID 到 T 的拥有型映射；值由 memnew 创建，移出映射后必须执行 on_destroy 再 memdelete。
	HashMap<GdObj, T *> id_objects;
	// 该类对象共用的场景根节点；加入树后由 SceneTree 拥有，本类只保存借用指针。
	Node2D *root = nullptr;

	// 检查调用是否位于 Godot 主线程；场景树和映射均不提供跨线程并发访问。
	bool _require_main_thread(const char *p_operation) const {
		if (likely(Thread::is_main_thread())) {
			return true;
		}
		ERR_PRINT(vformat("Object manager operation %s must run on the engine main thread.", p_operation));
		return false;
	}

	// 创建并登记一个 T，返回跨语言稳定 ID。
	// 直接调用方：具体 Manager 的 SPX_BIND create API；顶层调用方：Go 对象创建流程。
	GdObj _create_object() {
		if (unlikely(!_require_main_thread(__func__))) {
			return NULL_OBJECT_ID;
		}
		auto id = get_unique_id();
		T *object = memnew(T);
		object->on_create(id, root);
		id_objects[id] = object;
		return id;
	}

	// 不做线程检查的内部查找，仅允许已确认在主线程的同一调用栈中使用。
	T *_find_object(GdObj obj) const {
		T *const *object = id_objects.getptr(obj);
		return object != nullptr ? *object : nullptr;
	}

	// 带主线程检查的借用查找；返回值不延长 T 的生命周期。
	T *_find_object_checked(GdObj obj, const char *p_operation) const {
		if (unlikely(!_require_main_thread(p_operation))) {
			return nullptr;
		}
		return _find_object(obj);
	}

	// 创建对象共享根节点并挂到 SPX 根下。
	// Godot 规定：memnew 的 Node 应在主线程 add_child，进入树后由 SceneTree 管理生命周期。
	void _create_root(const String &name) {
		if (unlikely(!_require_main_thread(__func__))) {
			return;
		}
		root = memnew(Node2D);
		root->set_name(name);
		get_spx_root()->add_child(root);
	}

	// 释放全部 T，并对场景根节点调用 queue_free；由具体 Manager::on_destroy() 调用。
	void _destroy_objects_and_root();

	// 对当前仍存活对象执行一帧更新；由具体 Manager::on_update() 调用。
	void _update_all(float delta);

	// 通知并销毁当前局全部 T，但保留可复用的共享根节点；由 Manager::on_reset() 调用。
	void _reset_objects(int reset_code);

public:
	// 返回主线程当前任务内的借用指针；直接调用方：具体 Manager 的属性/命令 API。
	T *get_object(GdObj obj) {
		return _find_object_checked(obj, __func__);
	}

	// const 版本仍要求主线程，因为 reset/destroy 可能使对象失效。
	const T *get_object(GdObj obj) const {
		return _find_object_checked(obj, __func__);
	}

	// 在一次已检查的借用生命周期内执行操作，避免调用方长期保存裸指针。
	template <typename Func>
	bool with_object(GdObj obj, Func &&func) {
		T *object = _find_object_checked(obj, __func__);
		if (object == nullptr) {
			return false;
		}
		func(object);
		return true;
	}

	// 带返回值版本；对象无效或线程错误时返回调用方提供的默认值。
	template <typename Ret, typename Func>
	Ret with_object_ret(GdObj obj, Ret default_value, Func &&func) {
		T *object = _find_object_checked(obj, __func__);
		if (object == nullptr) {
			return default_value;
		}
		return func(object);
	}

	// 销毁单个对象；直接调用方：具体 Manager destroy API，顶层调用方：Go 对象销毁流程。
	void destroy_object(GdObj obj);
};

template <typename T>
void SpxObjectMgr<T>::_destroy_objects_and_root() {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}
	Vector<T *> objects;
	for (const KeyValue<GdObj, T *> &E : id_objects) {
		objects.push_back(E.value);
	}
	id_objects.clear();

	for (T *object : objects) {
		object->on_destroy();
		memdelete(object);
	}

	if (root) {
		// Godot 规定：树内 Node 不应在遍历/通知期间立即 memdelete，queue_free 会在安全时机删除。
		root->queue_free();
		root = nullptr;
	}
}

template <typename T>
void SpxObjectMgr<T>::_update_all(float delta) {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}
	Vector<GdObj> object_ids;
	for (const KeyValue<GdObj, T *> &E : id_objects) {
		object_ids.push_back(E.key);
	}

	// 每次用 ID 重新查找：某个对象的主线程回调可能销毁本轮稍后更新的另一个对象，
	// 重新解析可避免更新列表中遗留悬空指针。
	for (GdObj id : object_ids) {
		T *object = _find_object(id);
		if (object != nullptr) {
			object->on_update(delta);
		}
	}
}

template <typename T>
void SpxObjectMgr<T>::_reset_objects(int reset_code) {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}
	Vector<T *> objects;
	for (const KeyValue<GdObj, T *> &E : id_objects) {
		objects.push_back(E.value);
	}
	id_objects.clear();

	for (T *object : objects) {
		object->on_reset(reset_code);
		object->on_destroy();
		memdelete(object);
	}
}

template <typename T>
void SpxObjectMgr<T>::destroy_object(GdObj obj) {
	T *object = _find_object_checked(obj, __func__);
	if (object != nullptr) {
		id_objects.erase(obj);
		object->on_destroy();
		memdelete(object);
	}
}

#endif // SPX_OBJECT_MGR_H
