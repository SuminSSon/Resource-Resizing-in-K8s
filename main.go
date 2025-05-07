package main

import (
	"context"
	"fmt"
	"time"

	ai "mljob-controller/api/v1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	roleLabel   = "role"
	workLabel   = "workload"
	mljobLabel  = "mljob"
	roleActive  = "active"
	roleStandby = "standby"

	finalizer = "mljob.ai.mylab/cleanup"
)

// --------------------------------------------------
// Reconciler struct
// --------------------------------------------------

type MLJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config *rest.Config
}

// --------------------------------------------------
// Helpers
// --------------------------------------------------

func isAdmitted(wl *kueue.Workload) bool {
	if wl.Status.Admission == nil || len(wl.Status.Admission.PodSetAssignments) == 0 {
		return false
	}
	return true
}

func buildWorkload(job *ai.MLJob, name, role string) *kueue.Workload {
	cpu := job.Spec.CPU
	if cpu == "" {
		cpu = "1"
	}
	cpuQty := resource.MustParse(cpu)

	pvc := job.Spec.CheckpointPVC
	if pvc == "" {
		pvc = "checkpoint-pvc"
	}

	req := corev1.ResourceList{corev1.ResourceCPU: cpuQty}

	return &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: job.Namespace,
			Labels: map[string]string{
				roleLabel:  role,
				workLabel:  name,
				mljobLabel: job.Name,
			},
		},
		Spec: kueue.WorkloadSpec{
			QueueName: job.Spec.QueueName,
			PodSets: []kueue.PodSet{{
				Name:  "train",
				Count: 1,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							roleLabel:  role,
							workLabel:  name,
							mljobLabel: job.Name,
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "trainer",
							Image: job.Spec.Image,
							Resources: corev1.ResourceRequirements{
								Requests: req,
								Limits:   req,
							},
							VolumeMounts: []corev1.VolumeMount{{
								Name:      "ckpt",
								MountPath: job.Spec.CheckpointPath, // "/mnt/data/checkpoints"
							}},
						}},
						Volumes: []corev1.Volume{{
							Name: "ckpt",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvc,
								},
							},
						}},
					},
				},
			}},
		},
	}
}

func (r *MLJobReconciler) sendSIGUSR1(ctx context.Context, ns, pod string) {
	cmd := []string{"kill", "-SIGUSR1", "1"}
	restcli := kubernetes.NewForConfigOrDie(r.Config).CoreV1().RESTClient()
	req := restcli.Post().
		Namespace(ns).Resource("pods").Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   cmd,
			Container: "trainer",
		}, clientgoscheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(r.Config, "POST", req.URL())
	if err == nil {
		_ = exec.Stream(remotecommand.StreamOptions{})
	}
}

func (r *MLJobReconciler) ensurePod(ctx context.Context, wl *kueue.Workload) error {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(wl.Namespace),
		client.MatchingLabels{workLabel: wl.Name},
	); err != nil {
		return err
	}
	if len(pods.Items) > 0 {
		return nil
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: wl.Name + "-pod-",
			Namespace:    wl.Namespace,
			Labels:       wl.Labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(wl, kueue.SchemeGroupVersion.WithKind("Workload")),
			},
		},
		Spec: *wl.Spec.PodSets[0].Template.Spec.DeepCopy(),
	}
	return r.Create(ctx, pod)
}

func (r *MLJobReconciler) deleteWLandPods(ctx context.Context, wl *kueue.Workload) {
	prop := metav1.DeletePropagationBackground
	_ = r.Delete(ctx, wl, client.PropagationPolicy(prop))

	var pods corev1.PodList
	_ = r.List(ctx, &pods,
		client.InNamespace(wl.Namespace),
		client.MatchingLabels{workLabel: wl.Name},
	)
	for _, p := range pods.Items {
		_ = r.Delete(ctx, &p, client.PropagationPolicy(prop))
	}
}

// --------------------------------------------------
// Reconcile
// --------------------------------------------------

func (r *MLJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// 1) Load MLJob
	var job ai.MLJob
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2) Finalizer check for cleanup
	if job.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&job, finalizer) {
			controllerutil.AddFinalizer(&job, finalizer)
			_ = r.Update(ctx, &job)
		}
	} else {
		// deleting, cleanup Workloads and Pods
		var wls kueue.WorkloadList
		_ = r.List(ctx, &wls,
			client.InNamespace(job.Namespace),
			client.MatchingLabels{mljobLabel: job.Name},
		)
		for i := range wls.Items {
			r.deleteWLandPods(ctx, &wls.Items[i])
		}
		controllerutil.RemoveFinalizer(&job, finalizer)
		_ = r.Update(ctx, &job)
		return ctrl.Result{}, nil
	}

	// 3) Fetch current Workloads
	var wlList kueue.WorkloadList
	if err := r.List(ctx, &wlList,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{mljobLabel: job.Name},
	); err != nil {
		return ctrl.Result{}, err
	}

	var activeWL, standbyWL *kueue.Workload
	for i := range wlList.Items {
		w := &wlList.Items[i]
		switch w.Labels[roleLabel] {
		case roleActive:
			activeWL = w
		case roleStandby:
			standbyWL = w
		}
	}

	activeName := fmt.Sprintf("mljob-%s-active", job.Name)

	// 4) Create Active Workload if not exists
	if activeWL == nil {
		wl := buildWorkload(&job, activeName, roleActive)
		wl.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(&job, ai.SchemeGroupVersion.WithKind("MLJob"))}
		if err := r.Create(ctx, wl); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		log.Info("Active Workload created", "name", activeName)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 5) Check if Active is admitted and wait
	if !isAdmitted(activeWL) {
		log.Info("Waiting Active admission", "name", activeWL.Name)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Ensure the active pod is created
	if err := r.ensurePod(ctx, activeWL); err != nil {
		return ctrl.Result{}, err
	}

	// 6) Handle spec change (CPU change handling)
	needCPU := resource.MustParse("1")
	if job.Spec.CPU != "" {
		needCPU = resource.MustParse(job.Spec.CPU)
	}
	curCPU := activeWL.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
	if curCPU.Cmp(needCPU) == 0 {
		// nothing changed
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// 7) Create Standby Workload if needed
	if standbyWL == nil {
		name := fmt.Sprintf("mljob-%s-upgrade-%d", job.Name, job.Generation)
		st := buildWorkload(&job, name, roleStandby)
		st.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(&job, ai.SchemeGroupVersion.WithKind("MLJob"))}
		if err := r.Create(ctx, st); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		log.Info("Standby Workload created", "name", name)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 8) Check if Standby is admitted
	if !isAdmitted(standbyWL) {
		log.Info("Waiting Standby admission", "name", standbyWL.Name)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 9) Ensure standby pod is created
	if err := r.ensurePod(ctx, standbyWL); err != nil {
		return ctrl.Result{}, err
	}

	// 10) Check if Standby pod is ready, if not, continue waiting
	var spods corev1.PodList
	if err := r.List(ctx, &spods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{workLabel: standbyWL.Name},
	); err != nil {
		return ctrl.Result{}, err
	}
	ready := false
	for _, p := range spods.Items {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if ready {
			break
		}
	}
	if !ready {
		log.Info("Standby pod not ready yet", "workload", standbyWL.Name)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 11) Trigger checkpoint on active pods
	var apods corev1.PodList
	_ = r.List(ctx, &apods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{workLabel: activeWL.Name},
	)
	for _, p := range apods.Items {
		r.sendSIGUSR1(ctx, job.Namespace, p.Name)
	}

	// 12) Delete active Workload and Pods
	time.Sleep(3 * time.Second) // Grace period
	r.deleteWLandPods(ctx, activeWL)

	// 13) Promote Standby to Active by updating label
	patch := client.MergeFrom(standbyWL.DeepCopy())
	standbyWL.Labels[roleLabel] = roleActive
	if err := r.Patch(ctx, standbyWL, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Switchover done", "newActive", standbyWL.Name)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}


// --------------------------------------------------

func (r *MLJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ai.MLJob{}).
		Owns(&kueue.Workload{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

func main() {
	ctrl.SetLogger(ctrlzap.New(ctrlzap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	_ = ai.AddToScheme(scheme)
	_ = kueue.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	reconciler := &MLJobReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Config: mgr.GetConfig(),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}
}

