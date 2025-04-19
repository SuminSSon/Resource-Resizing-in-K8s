package main

import (
    "context"
    "time"
    "fmt"

    ai "mljob-controller/api/v1"
    kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/api/resource"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/apimachinery/pkg/runtime"

    "sigs.k8s.io/controller-runtime/pkg/client"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/log"
    ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type MLJobReconciler struct {
	client.Client
}

func getUniquePodName(workloadName string) string {
	return fmt.Sprintf("%s-pod-%d", workloadName, time.Now().UnixNano())
}

func isAdmitted(wl *kueuev1beta1.Workload) bool {
    if wl.Status.Admission == nil {
        return false
    }
    if len(wl.Status.Admission.PodSetAssignments) == 0 {
        return false
    }
    for _, assign := range wl.Status.Admission.PodSetAssignments {
        for _, ps := range wl.Spec.PodSets {
            if ps.Name == assign.Name && int(*assign.Count) == int(ps.Count) {
                return true
            }
        }
    }
    return false
}

func buildPodFromWorkload(wl *kueuev1beta1.Workload) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wl.Name + "-pod",
			Namespace: wl.Namespace,
			Labels: map[string]string{
				"workload": wl.Name,
			},
		},
		Spec: *wl.Spec.PodSets[0].Template.Spec.DeepCopy(),
	}
}

func (r *MLJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    // 1. MLJob 가져오기
    var mljob ai.MLJob
    if err := r.Get(ctx, req.NamespacedName, &mljob); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. 고정된 Workload 이름
    workloadName := "mljob-" + mljob.Name

    // 3. 해당 Workload 조회
    var workload kueuev1beta1.Workload
    err := r.Get(ctx, types.NamespacedName{Name: workloadName, Namespace: mljob.Namespace}, &workload)
    if err != nil {
        // 3‑1. Workload가 없으면 생성
        newWL := buildWorkloadFromMLJob(&mljob, workloadName)
        newWL.OwnerReferences = []metav1.OwnerReference{
            *metav1.NewControllerRef(&mljob, ai.SchemeGroupVersion.WithKind("MLJob")),
        }
        if err := r.Create(ctx, newWL); err != nil {
            logger.Error(err, "Workload 생성 실패")
            return ctrl.Result{}, err
        }
        logger.Info("새 Workload 생성 완료", "Workload", newWL.Name)
        return ctrl.Result{}, nil
    }

    // 3‑1b. MLJob.Spec.CPU 변경 감지
    existingCPU := workload.Spec.PodSets[0].Template.Spec.Containers[0].
        Resources.Limits[corev1.ResourceCPU]
    desiredCPU := resource.MustParse(mljob.Spec.CPU)
    if existingCPU.Cmp(desiredCPU) != 0 {
        logger.Info("MLJob CPU 변경 감지, 기존 Workload 삭제",
            "from", existingCPU.String(), "to", desiredCPU.String())
        if err := r.Delete(ctx, &workload, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
            logger.Error(err, "기존 Workload 삭제 실패")
            return ctrl.Result{}, err
        }
        return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
    }

    // 3‑1c. MLJob.Spec.GPU 변경 감지
    existingGPU := workload.Spec.PodSets[0].Template.Spec.Containers[0].
        Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]
    // 빈 문자열이나 "0"일 때는 0 GPU
    var desiredGPU resource.Quantity
    if mljob.Spec.GPU != "" {
        desiredGPU = resource.MustParse(mljob.Spec.GPU)
    }
    if existingGPU.Cmp(desiredGPU) != 0 {
        logger.Info("MLJob GPU 변경 감지, 기존 Workload 삭제",
            "from", existingGPU.String(), "to", desiredGPU.String())
        if err := r.Delete(ctx, &workload, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
            logger.Error(err, "기존 Workload 삭제 실패")
            return ctrl.Result{}, err
        }
        return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
    }

    // 4. Workload가 Admitted 되었는지 확인
    if isAdmitted(&workload) {
        // 4‑1. 매칭되는 Pod 목록 조회
        pods := &corev1.PodList{}
        if err := r.List(ctx, pods,
            client.InNamespace(mljob.Namespace),
            client.MatchingLabels{"workload": workload.Name},
        ); err != nil {
            return ctrl.Result{}, err
        }

        // 4‑2. Pod가 없으면(또는 generation mismatch) 재생성 플래그
        recreate := false
        if len(pods.Items) == 0 {
            recreate = true
        } else {
            for _, pod := range pods.Items {
                if pod.Labels["workload-gen"] != fmt.Sprint(workload.Generation) {
                    if err := r.Delete(ctx, &pod, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
                        logger.Error(err, "기존 Pod 삭제 실패", "Pod", pod.Name)
                        return ctrl.Result{}, err
                    }
                    logger.Info("기존 Pod 삭제 완료", "Pod", pod.Name)
                    recreate = true
                }
            }
        }

        // 4‑3. 필요시 새 Pod 생성
        if recreate {
            podName := fmt.Sprintf("%s-pod-%d", workload.Name, time.Now().UnixNano())
            newPod := buildPodFromWorkload(&workload)
            newPod.Name = podName
            newPod.Labels["workload"] = workload.Name
            newPod.Labels["workload-gen"] = fmt.Sprint(workload.Generation)
            newPod.OwnerReferences = []metav1.OwnerReference{
                *metav1.NewControllerRef(&workload, kueuev1beta1.SchemeGroupVersion.WithKind("Workload")),
            }
            if err := r.Create(ctx, newPod); err != nil {
                logger.Error(err, "새 Pod 생성 실패", "Pod", podName)
                return ctrl.Result{}, err
            }
            logger.Info("새 Pod 생성 완료", "Pod", newPod.Name)
        }
    }
    return ctrl.Result{}, nil
}


func buildWorkloadFromMLJob(job *ai.MLJob, workloadName string) *kueuev1beta1.Workload {
    // CPU가 비어 있으면 "1"로 기본값 대체
    cpuStr := job.Spec.CPU
    if cpuStr == "" {
        cpuStr = "1"
    }
    cpuQty := resource.MustParse(cpuStr)

    return &kueuev1beta1.Workload{
        ObjectMeta: metav1.ObjectMeta{
            Name:      workloadName,
            Namespace: job.Namespace,
        },
        Spec: kueuev1beta1.WorkloadSpec{
            QueueName: job.Spec.QueueName,
            PodSets: []kueuev1beta1.PodSet{{
                Name:  "train",
                Count: 1,
                Template: corev1.PodTemplateSpec{
                    Spec: corev1.PodSpec{
                        Containers: []corev1.Container{{
                            Name:  "trainer",
                            Image: job.Spec.Image,
                            Args:  []string{"--resume-from=" + job.Spec.CheckpointPath},
                            Resources: corev1.ResourceRequirements{
                                Requests: corev1.ResourceList{
                                    corev1.ResourceCPU: cpuQty,
                                },
                                Limits: corev1.ResourceList{
                                    corev1.ResourceCPU: cpuQty,
                                },
                            },
                            VolumeMounts: []corev1.VolumeMount{{
                                Name:      "ckpt",
                                MountPath: "/mnt/data/checkpoints",
                            }},
                        }},
                        Volumes: []corev1.Volume{{
                            Name: "ckpt",
                            VolumeSource: corev1.VolumeSource{
                                PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
                                    ClaimName: "checkpoint-pvc",
                                },
                            },
                        }},
                    },
                },
            }},
        },
    }
}

func (r *MLJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&ai.MLJob{}).
        Owns(&kueuev1beta1.Workload{}).
        Owns(&corev1.Pod{}).
        Complete(r)
}

func main() {
	log.SetLogger(ctrlzap.New(ctrlzap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := ai.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := kueuev1beta1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		panic(err)
	}
	if err = (&MLJobReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	panic(mgr.Start(ctrl.SetupSignalHandler()))
}

