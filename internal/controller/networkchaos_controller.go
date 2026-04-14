package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netchaossimulatorv1 "net-chaos-simulator/api/v1"
)

type NetworkChaosReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HTTPClient *http.Client
}

type ApplyLatencyRequest struct {
	ContainerID string `json:"container_id"`
	Delay       string `json:"delay"`
	TargetIP    string `json:"target_ip"`
}

const netChaosFinalizer = "net-chaos-simulator.pafev.dev/finalizer"

// +kubebuilder:rbac:groups=net-chaos-simulator.pafev.dev,resources=networkchaos,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=net-chaos-simulator.pafev.dev,resources=networkchaos/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=net-chaos-simulator.pafev.dev,resources=networkchaos/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

func (r *NetworkChaosReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var netChaos netchaossimulatorv1.NetworkChaos
	if err := r.Get(ctx, req.NamespacedName, &netChaos); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !netChaos.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &netChaos)
	}

	sourcePods, err := r.fetchSelectedPods(ctx, netChaos.Spec.SourceSelector, req.Namespace)
	if err != nil {
		logger.Error(err, "Erro ao buscar sourcePods")
		return ctrl.Result{}, err
	}

	targetPods, err := r.fetchSelectedPods(ctx, netChaos.Spec.TargetSelector, req.Namespace)
	if err != nil {
		logger.Error(err, "Erro ao buscar targetPods")
		return ctrl.Result{}, err
	}

	var targetIPs []string
	for _, pod := range targetPods {
		if pod.Status.PodIP != "" {
			targetIPs = append(targetIPs, pod.Status.PodIP)
		}
	}

	if len(targetIPs) == 0 {
		logger.Info("Nenhum IP de destino encontrado ainda. Tentando novamente mais tarde.")
		return ctrl.Result{RequeueAfter: 5}, nil
	}

	for _, sourcePod := range sourcePods {
		if sourcePod.Status.Phase != corev1.PodRunning || len(sourcePod.Status.ContainerStatuses) == 0 {
			continue
		}

		containerID := sourcePod.Status.ContainerStatuses[0].ContainerID // Pega o principal
		nodeName := sourcePod.Spec.NodeName

		var agentPods corev1.PodList
		err := r.List(ctx, &agentPods,
			client.InNamespace("net-chaos-system"),
			client.MatchingLabels{"app": "net-agent"},
			client.MatchingFields{"spec.nodeName": nodeName},
		)

		if err != nil || len(agentPods.Items) == 0 {
			logger.Error(err, "Agente net-agent não encontrado no node", "node", nodeName)
			continue
		}

		agentIP := agentPods.Items[0].Status.PodIP

		for _, targetIP := range targetIPs {
			err := r.sendLatencyRequest(agentIP, containerID, netChaos.Spec.Delay, targetIP)
			if err != nil {
				logger.Error(err, "Falha ao aplicar latência", "pod", sourcePod.Name)
			} else {
				logger.Info("Latência aplicada com sucesso", "pod", sourcePod.Name, "target", targetIP)
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *NetworkChaosReconciler) reconcileDelete(ctx context.Context, netChaos *netchaossimulatorv1.NetworkChaos) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(netChaos, netChaosFinalizer) {
		if err := r.deleteLatencyOnNodes(ctx, netChaos); err != nil {
			return ctrl.Result{}, err
		}

		controllerutil.RemoveFinalizer(netChaos, netChaosFinalizer)
		if err := r.Update(ctx, netChaos); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *NetworkChaosReconciler) fetchSelectedPods(ctx context.Context, selector netchaossimulatorv1.PodSelector, crdNamespace string) ([]corev1.Pod, error) {
	targetNamespace := crdNamespace
	if selector.Namespace != "" {
		targetNamespace = selector.Namespace
	}

	var pods []corev1.Pod

	if selector.Name != "" {
		var pod corev1.Pod
		err := r.Get(ctx, client.ObjectKey{Name: selector.Name, Namespace: targetNamespace}, &pod)
		if err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		pods = append(pods, pod)
		return pods, nil
	}

	if selector.Labels != nil {
		labelSelector, err := metav1.LabelSelectorAsSelector(selector.Labels)
		if err != nil {
			return nil, err
		}

		var podList corev1.PodList
		err = r.List(ctx, &podList, client.InNamespace(targetNamespace), client.MatchingLabelsSelector{Selector: labelSelector})
		if err != nil {
			return nil, err
		}
		return podList.Items, nil
	}

	return pods, nil
}

func (r *NetworkChaosReconciler) sendLatencyRequest(agentIP, containerID, delay, targetIP string) error {
	url := fmt.Sprintf("http://%s:8080/api/apply-latency", agentIP)

	payload := ApplyLatencyRequest{
		ContainerID: containerID,
		Delay:       delay,
		TargetIP:    targetIP,
	}

	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemonset retornou status %d", resp.StatusCode)
	}

	return nil
}

func (r *NetworkChaosReconciler) deleteLatencyOnNodes(ctx context.Context, netChaos *netchaossimulatorv1.NetworkChaos) error {
	logger := logf.FromContext(ctx)

	sourcePods, err := r.fetchSelectedPods(ctx, netChaos.Spec.SourceSelector, netChaos.Namespace)
	if err != nil {
		return err
	}

	targetPods, err := r.fetchSelectedPods(ctx, netChaos.Spec.TargetSelector, netChaos.Namespace)
	if err != nil {
		return err
	}

	var targetIPs []string
	for _, pod := range targetPods {
		if pod.Status.PodIP != "" {
			targetIPs = append(targetIPs, pod.Status.PodIP)
		}
	}

	// DICA DE ARQUITETURA: Se o usuário deletar os pods de destino ANTES de deletar o NetworkChaos,
	// não teremos os IPs para limpar a regra específica. Nesse caso, usamos a rota de fallback (clear-latency)
	// que você já havia inteligentemente criado no DaemonSet.
	useClearFallback := len(targetIPs) == 0

	for _, sourcePod := range sourcePods {
		if sourcePod.Status.Phase != corev1.PodRunning || len(sourcePod.Status.ContainerStatuses) == 0 {
			continue
		}

		containerID := sourcePod.Status.ContainerStatuses[0].ContainerID
		nodeName := sourcePod.Spec.NodeName

		var agentPods corev1.PodList
		err := r.List(ctx, &agentPods,
			client.InNamespace("net-chaos-system"),
			client.MatchingLabels{"app": "net-agent"},
			client.MatchingFields{"spec.nodeName": nodeName},
		)

		if err != nil || len(agentPods.Items) == 0 {
			logger.Error(err, "Agente net-agent não encontrado no node durante a limpeza", "node", nodeName)
			continue
		}

		agentIP := agentPods.Items[0].Status.PodIP

		if useClearFallback {
			logger.Info("Nenhum IP de destino encontrado. Executando fallback de limpeza total no pod.", "pod", sourcePod.Name)
			r.sendClearLatencyRequest(agentIP, containerID)
			continue
		}

		// Faz a requisição HTTP DELETE para cada targetIP encontrado
		for _, targetIP := range targetIPs {
			err := r.sendDeleteLatencyRequest(agentIP, containerID, targetIP)
			if err != nil {
				logger.Error(err, "Falha ao remover latência específica", "pod", sourcePod.Name, "targetIP", targetIP)
			} else {
				logger.Info("Latência removida com sucesso", "pod", sourcePod.Name, "target", targetIP)
			}
		}
	}

	return nil
}

func (r *NetworkChaosReconciler) sendDeleteLatencyRequest(agentIP, containerID, targetIP string) error {
	url := fmt.Sprintf("http://%s:8080/api/delete-latency", agentIP)

	payload := map[string]string{
		"container_id": containerID,
		"target_ip":    targetIP,
	}

	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodDelete, url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemonset retornou status %d ao deletar latência", resp.StatusCode)
	}

	return nil
}

func (r *NetworkChaosReconciler) sendClearLatencyRequest(agentIP, containerID string) error {
	url := fmt.Sprintf("http://%s:8080/api/clear-latency", agentIP)

	payload := map[string]string{
		"container_id": containerID,
	}

	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodDelete, url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemonset retornou status %d no fallback de clear-latency", resp.StatusCode)
	}

	return nil
}

func (r *NetworkChaosReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&netchaossimulatorv1.NetworkChaos{}).
		Named("networkchaos").
		Complete(r)
}
